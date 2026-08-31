// Acceptance test for the langsmith_workspace_secrets data source. See
// workspace_secret_resource_test.go for how these are run.

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	keyZulu  = "TF_ACC_WORKSPACE_SECRETS_DATASOURCE_ZULU"
	keyAlpha = "TF_ACC_WORKSPACE_SECRETS_DATASOURCE_ALPHA"
	keyMike  = "TF_ACC_WORKSPACE_SECRETS_DATASOURCE_MIKE"

	workspaceSecretsDS    = "data.langsmith_workspace_secrets.current"
	workspaceSecretsDSCfg = `data "langsmith_workspace_secrets" "current" {}`
)

// TestAccWorkspaceSecretsDataSource seeds secrets through the API and checks
// the data source reports them.
func TestAccWorkspaceSecretsDataSource(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run workspace secrets data source test")
	}

	seeded := []string{keyZulu, keyAlpha, keyMike}
	t.Cleanup(func() {
		for _, key := range seeded {
			if err := deleteWorkspaceSecretOutOfBand(key); err != nil {
				t.Errorf("cleanup of %s failed: %v", key, err)
			}
		}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					for _, key := range seeded {
						if err := upsertWorkspaceSecretOutOfBand(key, "dummy-value-1"); err != nil {
							t.Fatalf("seeding %s failed: %v", key, err)
						}
					}
				},
				Config: workspaceSecretsDSCfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(workspaceSecretsDS, tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(workspaceSecretsDS, tfjsonpath.New("keys"), knownvalue.SetPartial([]knownvalue.Check{
						knownvalue.StringExact(keyZulu),
						knownvalue.StringExact(keyAlpha),
						knownvalue.StringExact(keyMike),
					})),
				},
			},
		},
	})
}
