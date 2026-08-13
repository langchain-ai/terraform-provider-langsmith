// Acceptance tests for the langsmith_gateway_policy resource.
//
// These drive real Terraform (plan/apply/destroy) through the provider against a
// live LangSmith, skipped by default.
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run '^TestAccGatewayPolicy' -count=1 -v

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/langchain-ai/langsmith-go"
)

const gatewayPolicySpendCapAcceptanceConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-spend-cap"
  description = "created by TestAccGatewayPolicySpendCap"
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
			{
				Config: gatewayPolicySpendCapAcceptanceConfig,
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
					resource.TestCheckResourceAttrSet("langsmith_gateway_policy.test", "created_by"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "parent_policy_id"),
				),
			},
			{
				ResourceName:      "langsmith_gateway_policy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config:  gatewayPolicySpendCapAcceptanceConfig,
				Destroy: true,
			},
		},
	})
	// check the resource was deleted from the API.
	var apiResp gatewayPolicyGetAPI
	err := langsmith.NewClient().Get(
		context.Background(),
		fmt.Sprintf("api/v1/platform/gateway-policies/%s", policyID),
		nil,
		&apiResp,
	)
	if err == nil {
		t.Fatalf("gateway policy %s still exists after destroy", policyID)
	}
	if !isLangSmithNotFound(err) {
		t.Fatalf("read gateway policy %s after destroy: %v", policyID, err)
	}
}
