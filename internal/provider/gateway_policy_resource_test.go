// Acceptance tests for the langsmith_gateway_policy resource.
//
// These drive real Terraform (plan/apply/destroy) through the provider against a
// live LangSmith, skipped by default.
//
// Example:
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run TestAccGatewayPolicySpendCapCreate -count=1 -v

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const gatewayPolicySpendCapAcceptanceConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-spend-cap"
  description = "created by TestAccGatewayPolicySpendCapCreate"
  policy_type = "spend_cap"
  action      = "block"
  # enabled / priority omitted → schema defaults (true / 0)

  config = {
    spend_cap = {
      window    = "monthly"
      limit_usd = 12.5
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = "00000000-0000-4000-8000-000000000001"
  }]
}
`

// TestAccGatewayPolicySpendCapCreate exercises Terraform create (and destroy) of a
// spend_cap gateway policy against a live LangSmith. See the file-level comment
// for how to run it.
func TestAccGatewayPolicySpendCapCreate(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run spend_cap gateway policy create smoke test")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: gatewayPolicySpendCapAcceptanceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("langsmith_gateway_policy.test", "id"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "created by TestAccGatewayPolicySpendCapCreate"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "true"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "priority", "0"),
					resource.TestCheckResourceAttrSet("langsmith_gateway_policy.test", "created_by"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "parent_policy_id"),
				),
			},
		},
	})
}
