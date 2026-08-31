// Acceptance tests for the langsmith_workspace_secret resource.
//
// These drive real Terraform (plan/apply/destroy) through the provider against a
// live LangSmith, skipped by default. The secret values below are obvious dummies
// on purpose: value is persisted to the test's state file.
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run '^TestAccWorkspaceSecret' -count=1 -v

package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

// TestAccWorkspaceSecretLifecycle walks one secret through adoption, the
// already-exists guard, import, update, drift, and a key change.
//
// Step order is load-bearing:
//   - ImportStatePersist needs an address not already in state, so it precedes
//     any create.
//   - ImportStateVerify compares against prior state, so it comes after.
func TestAccWorkspaceSecretLifecycle(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run workspace secret lifecycle test")
	}

	const key = "TF_ACC_WORKSPACE_SECRET_LIFECYCLE"
	const renamedKey = "TF_ACC_WORKSPACE_SECRET_LIFECYCLE_RENAMED"

	// key is seeded out of band and unmanaged until step two adopts it.
	// Both are removed on a successful run; this covers a failure part way.
	t.Cleanup(func() {
		for _, k := range []string{key, renamedKey} {
			if err := deleteWorkspaceSecretOutOfBand(k); err != nil {
				t.Errorf("cleanup of %s failed: %v", k, err)
			}
		}
	})

	cfg := func(key, value string) string {
		return fmt.Sprintf(`
resource "langsmith_workspace_secret" "test" {
  key   = %q
  value = %q
}
`, key, value)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			// Create against an existing secret is blocked.
			{
				PreConfig: func() {
					if err := upsertWorkspaceSecretOutOfBand(key, "set-by-someone-else"); err != nil {
						t.Fatalf("out-of-band create failed: %v", err)
					}
				},
				Config:      cfg(key, "dummy-value-1"),
				ExpectError: regexp.MustCompile(`Workspace secret already exists`),
			},
			// Import and persist to state.
			{
				Config:             cfg(key, "dummy-value-1"),
				ResourceName:       "langsmith_workspace_secret.test",
				ImportState:        true,
				ImportStateId:      key,
				ImportStatePersist: true,
			},
			// State holds a null value, so the same config plans an update.
			{
				Config: cfg(key, "dummy-value-1"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("langsmith_workspace_secret.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "id", key),
					resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "key", key),
					resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "value", "dummy-value-1"),
				),
			},
			// New plan, no diff.
			{
				Config:             cfg(key, "dummy-value-1"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			// import, now with prior state to compare against. Not persisted.
			{
				ResourceName:            "langsmith_workspace_secret.test",
				ImportState:             true,
				ImportStateId:           key,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"value"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got, ok := states[0].Attributes["value"]; ok {
						return fmt.Errorf("value = %q, want it absent from imported state", got)
					}
					return nil
				},
			},
			// update in place
			{
				Config: cfg(key, "dummy-value-2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "id", key),
					resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "value", "dummy-value-2"),
				),
			},
			// drift: a key deleted out of band drops out of state, so the plan is
			// a create. A changed value cannot be detected.
			{
				PreConfig: func() {
					if err := deleteWorkspaceSecretOutOfBand(key); err != nil {
						t.Fatalf("out-of-band delete failed: %v", err)
					}
				},
				Config: cfg(key, "dummy-value-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("langsmith_workspace_secret.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "id", key),
			},
			// key change triggers replace
			{
				Config: cfg(renamedKey, "dummy-value-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("langsmith_workspace_secret.test", plancheck.ResourceActionReplace),
					},
				},
				Check: resource.TestCheckResourceAttr("langsmith_workspace_secret.test", "id", renamedKey),
			},
		},
	})
}

// deleteWorkspaceSecretOutOfBand simulates someone deleting a secret in the UI.
func deleteWorkspaceSecretOutOfBand(key string) error {
	return writeWorkspaceSecretOutOfBand(key, nil)
}

// upsertWorkspaceSecretOutOfBand simulates a secret that exists before
// Terraform manages it.
func upsertWorkspaceSecretOutOfBand(key string, value string) error {
	return writeWorkspaceSecretOutOfBand(key, &value)
}

// writeWorkspaceSecretOutOfBand posts one upsert, bypassing the provider. A nil
// value deletes the key. resolveAPIURL keeps it on the provider's endpoint.
func writeWorkspaceSecretOutOfBand(key string, value *string) error {
	var opts []option.RequestOption
	if endpoint := resolveAPIURL(""); endpoint != "" {
		opts = append(opts, option.WithBaseURL(endpoint))
	}
	client := langsmith.NewClient(opts...)
	body := []workspaceSecretUpsertAPI{{Key: key, Value: value}}
	return client.Post(context.Background(), workspaceSecretsPath, body, nil)
}
