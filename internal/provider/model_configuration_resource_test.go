// Acceptance tests for the langsmith_model_configuration resource.
//
// These drive real Terraform (plan/apply/destroy) through the provider against a
// live LangSmith, skipped by default.
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run '^TestAccModelConfiguration' -count=1 -v

package provider

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccModelConfigurationLifecycle covers create, import, and an in-place update
// of a workspace-scoped model configuration.
func TestAccModelConfigurationLifecycle(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run model configuration lifecycle test")
	}

	const name = "tf-acc model configuration lifecycle"
	const updatedName = "tf-acc model configuration lifecycle (updated)"
	const baseURL = "https://my-openai-proxy.example.com/v1"

	createCfg := fmt.Sprintf(`
resource "langsmith_model_configuration" "test" {
  name           = %q
  model_provider = "openai"
  model          = "gpt-4o"
  env_var_name   = "OPENAI_API_KEY"

  invocation_params = jsonencode({
    temperature = 0.7
  })
}
`, name)
	updateCfg := fmt.Sprintf(`
resource "langsmith_model_configuration" "test" {
  name           = %q
  model_provider = "openai"
  model          = "gpt-4o-mini"
  env_var_name   = "OPENAI_API_KEY"
  base_url       = %q

  invocation_params = jsonencode({
    temperature = 0.9
  })
}
`, updatedName, baseURL)

	var originalID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			// create
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_model_configuration.test", "id", func(value string) error {
						if value == "" {
							return fmt.Errorf("id is empty")
						}
						originalID = value
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "name", name),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model_provider", "openai"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model", "gpt-4o"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "env_var_name", "OPENAI_API_KEY"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "scope", modelConfigScopeWorkspace),
					resource.TestCheckNoResourceAttr("langsmith_model_configuration.test", "organization_id"),
					resource.TestCheckNoResourceAttr("langsmith_model_configuration.test", "base_url"),
					resource.TestCheckResourceAttrSet("langsmith_model_configuration.test", "created_at"),
					resource.TestCheckResourceAttrSet("langsmith_model_configuration.test", "updated_at"),
				),
			},
			// import: invocation_params is write-only and never echoed back by Read,
			// so re-importing a config that set it always disagrees with prior state.
			// See TestAccModelConfigurationImportNoDiffWithoutInvocationParams for the
			// null-in-config case, where import produces no diff at all.
			{
				ResourceName:            "langsmith_model_configuration.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"invocation_params"},
			},
			// update in place: no attribute changed here has a RequiresReplace modifier.
			{
				Config: updateCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_model_configuration.test", "id", func(value string) error {
						if value != originalID {
							return fmt.Errorf("id = %q, want %q (in-place update)", value, originalID)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "name", updatedName),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model", "gpt-4o-mini"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "base_url", baseURL),
				),
			},
			// Delete testing automatically occurs in TestCase for successful creates.
		},
	})
}

// TestAccModelConfigurationImportNoDiffWithoutInvocationParams verifies the
// other half of invocation_params' write-only behavior: when configuration
// never sets it, state is already null before and after import, so import
// produces no diff at all (unlike TestAccModelConfigurationLifecycle's import
// step, which must ignore invocation_params because config set it there).
func TestAccModelConfigurationImportNoDiffWithoutInvocationParams(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run model configuration import no-diff test")
	}

	const createCfg = `
resource "langsmith_model_configuration" "test" {
  name           = "tf-acc model configuration import no diff"
  model_provider = "openai"
  model          = "gpt-4o"
  env_var_name   = "OPENAI_API_KEY"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check:  resource.TestCheckNoResourceAttr("langsmith_model_configuration.test", "invocation_params"),
			},
			// import: no ImportStateVerifyIgnore needed, since invocation_params is
			// already null in both prior state and the freshly imported state.
			{
				ResourceName:      "langsmith_model_configuration.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// plan again post-import with the same config: assert zero diff, the
			// same convergence the write-only docs promise for the omitted case.
			{
				Config:             createCfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccModelConfigurationAzureBaseURL covers azure_openai, the one provider
// with no dedicated base URL kwarg: base_url round-trips through the server's
// azure_endpoint fallback instead (see modelConfigurationSettingsInfoFromAPI's
// azure_endpoint branch), which no other test in this file exercises.
func TestAccModelConfigurationAzureBaseURL(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run model configuration azure base url test")
	}

	const name = "tf-acc model configuration azure base url"
	const baseURL = "https://my-resource.openai.azure.com"

	createCfg := fmt.Sprintf(`
resource "langsmith_model_configuration" "test" {
  name           = %q
  model_provider = "azure_openai"
  model          = "gpt-4o"
  env_var_name   = "AZURE_OPENAI_API_KEY"
  base_url       = %q

  invocation_params = jsonencode({
    deployment_name    = "my-gpt4o-deployment"
    openai_api_version = "2024-02-15-preview"
  })
}
`, name, baseURL)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("langsmith_model_configuration.test", "id"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model_provider", "azure_openai"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model", "gpt-4o"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "env_var_name", "AZURE_OPENAI_API_KEY"),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "base_url", baseURL),
				),
			},
			{
				ResourceName:            "langsmith_model_configuration.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"invocation_params"},
			},
		},
	})
}

// TestAccModelConfigurationComplexWritePath covers the providers written as a
// SerializedConstructor rather than the simple modelId shorthand. Import is the
// point of it: it is the only check that the complex writer and the complex read
// decoder agree on where model, the secret reference and base_url live.
func TestAccModelConfigurationComplexWritePath(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run model configuration complex write path test")
	}

	tests := []struct {
		provider     string
		model        string
		updatedModel string
		envVarName   string
		// baseURL is empty for providers with no base URL kwarg, whose base_url
		// the writer has nowhere to put.
		baseURL string
	}{
		{
			provider:     "google_genai",
			model:        "gemini-3.1-pro-preview",
			updatedModel: "gemini-3.5-flash-lite",
			envVarName:   "GOOGLE_API_KEY",
			baseURL:      "https://my-gemini-proxy.example.com",
		},
		{
			// credentials holds a service-account JSON blob, not an API key.
			provider:     "google_vertexai",
			model:        "gemini-3.1-pro-preview",
			updatedModel: "gemini-2.0-flash-exp",
			envVarName:   "GOOGLE_VERTEX_AI_WEB_CREDENTIALS",
		},
		{
			provider:     "databricks",
			model:        "databricks-claude-sonnet-5",
			updatedModel: "databricks-claude-3-7-sonnet",
			envVarName:   "DATABRICKS_TOKEN",
		},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			t.Parallel()

			baseURLLine := ""
			if tt.baseURL != "" {
				baseURLLine = fmt.Sprintf("base_url = %q", tt.baseURL)
			}
			cfg := func(model string) string {
				return fmt.Sprintf(`
resource "langsmith_model_configuration" "test" {
  name           = "tf-acc model configuration complex %s"
  model_provider = %q
  model          = %q
  env_var_name   = %q
  %s
}
`, tt.provider, tt.provider, model, tt.envVarName, baseURLLine)
			}

			baseURLCheck := resource.TestCheckNoResourceAttr("langsmith_model_configuration.test", "base_url")
			if tt.baseURL != "" {
				baseURLCheck = resource.TestCheckResourceAttr("langsmith_model_configuration.test", "base_url", tt.baseURL)
			}

			var originalID string
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"langsmith": providerserver.NewProtocol6WithError(New("test")()),
				},
				Steps: []resource.TestStep{
					// create
					{
						Config: cfg(tt.model),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrWith("langsmith_model_configuration.test", "id", func(value string) error {
								if value == "" {
									return fmt.Errorf("id is empty")
								}
								originalID = value
								return nil
							}),
							resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model_provider", tt.provider),
							resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model", tt.model),
							resource.TestCheckResourceAttr("langsmith_model_configuration.test", "env_var_name", tt.envVarName),
							resource.TestCheckResourceAttr("langsmith_model_configuration.test", "scope", modelConfigScopeWorkspace),
							baseURLCheck,
							resource.TestCheckResourceAttrSet("langsmith_model_configuration.test", "created_at"),
						),
					},
					// import: configuration never sets invocation_params, so no
					// ImportStateVerifyIgnore is needed and any drift is a real
					// disagreement between the writer and the decoder.
					{
						ResourceName:      "langsmith_model_configuration.test",
						ImportState:       true,
						ImportStateVerify: true,
					},
					// update in place
					{
						Config: cfg(tt.updatedModel),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrWith("langsmith_model_configuration.test", "id", func(value string) error {
								if value != originalID {
									return fmt.Errorf("id = %q, want %q (in-place update)", value, originalID)
								}
								return nil
							}),
							resource.TestCheckResourceAttr("langsmith_model_configuration.test", "model", tt.updatedModel),
							baseURLCheck,
						),
					},
				},
			})
		})
	}
}

// TestAccModelConfigurationInvalidInvocationParams verifies that a non-object
// invocation_params value fails at apply time via canonicalJSONObject, rather
// than being silently coerced or accepted.
func TestAccModelConfigurationInvalidInvocationParams(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run model configuration invalid invocation_params test")
	}

	const invalidCfg = `
resource "langsmith_model_configuration" "test" {
  name              = "tf-acc model configuration invalid invocation_params"
  model_provider    = "openai"
  model             = "gpt-4o"
  env_var_name      = "OPENAI_API_KEY"
  invocation_params = jsonencode(["not", "an", "object"])
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config:      invalidCfg,
				ExpectError: regexp.MustCompile(regexp.QuoteMeta("invocation_params must be a JSON object")),
			},
		},
	})
}

// TestAccModelConfigurationScopeReplace verifies that changing scope from
// workspace to organization replaces the resource (scope has a RequiresReplace
// plan modifier since the API has no field to change it after creation).
func TestAccModelConfigurationScopeReplace(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run model configuration scope replace test")
	}

	const name = "tf-acc model configuration scope replace"

	workspaceCfg := fmt.Sprintf(`
resource "langsmith_model_configuration" "test" {
  name           = %q
  model_provider = "anthropic"
  model          = "claude-sonnet-5"
  env_var_name   = "ANTHROPIC_API_KEY"
  scope          = "workspace"
}
`, name)
	organizationCfg := fmt.Sprintf(`
resource "langsmith_model_configuration" "test" {
  name           = %q
  model_provider = "anthropic"
  model          = "claude-sonnet-5"
  env_var_name   = "ANTHROPIC_API_KEY"
  scope          = "organization"
}
`, name)

	var originalID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			// create workspace-scoped
			{
				Config: workspaceCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_model_configuration.test", "id", func(value string) error {
						if value == "" {
							return fmt.Errorf("id is empty")
						}
						originalID = value
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "scope", modelConfigScopeWorkspace),
					resource.TestCheckNoResourceAttr("langsmith_model_configuration.test", "organization_id"),
				),
			},
			// changing scope requires replace: expect a new id and organization_id set.
			{
				Config: organizationCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_model_configuration.test", "id", func(value string) error {
						if value == originalID {
							return fmt.Errorf("id = %q, want replace (new id)", value)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_model_configuration.test", "scope", modelConfigScopeOrganization),
					resource.TestCheckResourceAttrSet("langsmith_model_configuration.test", "organization_id"),
				),
			},
		},
	})
}
