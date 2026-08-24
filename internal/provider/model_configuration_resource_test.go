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
			// import: invocation_params is write-only and never echoed back by Read.
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
