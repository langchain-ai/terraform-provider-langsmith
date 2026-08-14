// Acceptance tests for the langsmith_gateway_policy resource.
//
// These drive real Terraform (plan/apply/destroy) through the provider against a
// live LangSmith, skipped by default.
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run '^TestAccGatewayPolicy' -count=1 -v

package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const gatewayPolicySpendCapConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-spend-cap"
  description = "created by TestAccGatewayPolicySpendCap"
  policy_type = "spend_cap"
  action      = "block"
  # omit enabled/priority to exercise schema defaults

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

const gatewayPolicySpendCapConfigUpdated = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-spend-cap-updated"
  description = "updated by TestAccGatewayPolicySpendCap"
  policy_type = "spend_cap"
  action      = "block"
  enabled     = false

  config = {
    spend_cap = {
      window    = "weekly"
      limit_usd = 25
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = "00000000-0000-4000-8000-000000000001"
  }]
}
`

// TestAccGatewayPolicySpendCap exercises Terraform lifecycle.
func TestAccGatewayPolicySpendCap(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run spend_cap gateway policy smoke test")
	}

	var policyID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			// create
			{
				Config: gatewayPolicySpendCapConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
						if value == "" {
							return fmt.Errorf("id is empty")
						}
						policyID = value
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "created by TestAccGatewayPolicySpendCap"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "true"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "priority", "0"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.spend_cap.window", "monthly"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.spend_cap.limit_usd", "12.5"),
					resource.TestCheckResourceAttrSet("langsmith_gateway_policy.test", "created_by"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "parent_policy_id"),
				),
			},
			// import
			{
				ResourceName:      "langsmith_gateway_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// update
			{
				Config: gatewayPolicySpendCapConfigUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
						if value != policyID {
							return fmt.Errorf("id = %q, want %q", value, policyID)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "name", "tf-provider-gateway-policy-spend-cap-updated"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "updated by TestAccGatewayPolicySpendCap"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "false"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.spend_cap.window", "weekly"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.spend_cap.limit_usd", "25"),
				),
			},
			// destroy. The API client will error if delete fails, and the Delete() method will add an error to the response,
			// failing the test.
			{
				Config:  gatewayPolicySpendCapConfigUpdated,
				Destroy: true,
			},
		},
	})
}
