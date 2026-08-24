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
  action      = "block"
  enabled     = false

  config = {
    spend_cap = {
      window    = "weekly"
      limit_usd = 25
    }
  }

  subject_matchers = [
    {
      key   = "workspace_id"
      value = "00000000-0000-4000-8000-000000000001"
    },
    {
      key   = "workspace_id"
      value = "00000000-0000-4000-8000-000000000002"
    },
  ]
}
`

const gatewayPolicyDefaultSpendCapConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-spend-cap"
  description = "created by TestAccGatewayPolicySpendCap"
  action      = "block"
  # omit enabled/priority to exercise schema defaults

  config = {
    default_spend_cap = {
      window    = "monthly"
      limit_usd = 12.5
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = ""
  }]
}
`

const gatewayPolicyDefaultSpendCapConfigUpdated = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-spend-cap-updated"
  description = "updated by TestAccGatewayPolicySpendCap"
  action      = "block"
  enabled     = false

  config = {
    default_spend_cap = {
      window    = "weekly"
      limit_usd = 25
    }
  }

  subject_matchers = [
    {
      key   = "workspace_id"
      value = ""
    }
  ]
}
`

// TestAccGatewayPolicySpendCap exercises Terraform lifecycle.
func TestAccGatewayPolicySpendCap(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run spend_cap gateway policy smoke test")
	}

	tests := []struct {
		policy_type string
		isDefault   bool
		original    string
		update      string
	}{
		{
			policy_type: "spend_cap",
			isDefault:   false,
			original:    gatewayPolicySpendCapConfig,
			update:      gatewayPolicySpendCapConfigUpdated,
		},
		{
			policy_type: "default_spend_cap",
			isDefault:   true,
			original:    gatewayPolicyDefaultSpendCapConfig,
			update:      gatewayPolicyDefaultSpendCapConfigUpdated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.policy_type, func(t *testing.T) {
			var policyID string
			count := "2"
			if tt.isDefault {
				count = "1"
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"langsmith": providerserver.NewProtocol6WithError(New("test")()),
				},
				Steps: []resource.TestStep{
					// create
					{
						Config: tt.original,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
								if value == "" {
									return fmt.Errorf("id is empty")
								}
								policyID = value
								return nil
							}),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "created by TestAccGatewayPolicySpendCap"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "policy_type", tt.policy_type),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "true"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "priority", "0"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".window", "monthly"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limit_usd", "12.5"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.#", "1"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.key", "workspace_id"),
							func() resource.TestCheckFunc {
								targetValue := "00000000-0000-4000-8000-000000000001"
								if tt.isDefault {
									targetValue = ""
								}
								return resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.value", targetValue)
							}(),
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
						Config: tt.update,
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
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".window", "weekly"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limit_usd", "25"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.#", count),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.key", "workspace_id"),
							func() resource.TestCheckFunc {
								targetValue := "00000000-0000-4000-8000-000000000001"
								if tt.isDefault {
									targetValue = ""
								}
								return resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.value", targetValue)
							}(),
							// default_spend_cap's updated config only has one matcher, since a second
							// {key: "workspace_id", value: ""} would be a duplicate list value.
							func() resource.TestCheckFunc {
								if tt.isDefault {
									return resource.ComposeAggregateTestCheckFunc()
								}
								return resource.ComposeAggregateTestCheckFunc(
									resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.1.key", "workspace_id"),
									resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.1.value", "00000000-0000-4000-8000-000000000002"),
								)
							}(),
						),
					},
					// Delete testing automatically occurs in TestCase
					// The API client will error if delete fails, and the Delete() method will add an error to the response,
					// failing the test.
				},
			})
		})
	}
}

const gatewayPolicyRateLimitConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-rate-limit"
  description = "created by TestAccGatewayPolicyRateLimit"
  action      = "block"
  # omit enabled/priority to exercise schema defaults

  config = {
    rate_limit = {
      version = 1
      limits = [
        {
          metric = "requests"
          window = "minute"
          value  = 25
        },
        {
          metric = "tokens"
          window = "hour"
          value  = 2000000
        },
      ]
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = "00000000-0000-4000-8000-000000000005"
  }]
}
`

const gatewayPolicyRateLimitConfigUpdated = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-rate-limit-updated"
  description = "updated by TestAccGatewayPolicyRateLimit"
  action      = "block"
  enabled     = false

  config = {
    rate_limit = {
      version = 1
      limits = [
        {
          metric = "requests"
          window = "hour"
          value  = 1500
        },
        {
          metric = "tokens"
          window = "hour"
          value  = 2000000
        },
        {
          metric = "requests"
          window = "minute"
          value  = 100
        },
      ]
    }
  }

  subject_matchers = [
    {
      key   = "workspace_id"
      value = "00000000-0000-4000-8000-000000000005"
    },
    {
      key   = "workspace_id"
      value = "00000000-0000-4000-8000-000000000006"
    },
  ]
}
`

const gatewayPolicyDefaultRateLimitConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-default-rate-limit"
  description = "created by TestAccGatewayPolicyRateLimit"
  action      = "block"
  # omit enabled/priority to exercise schema defaults

  config = {
    default_rate_limit = {
      version = 1
      limits = [
        {
          metric = "requests"
          window = "minute"
          value  = 25
        },
        {
          metric = "tokens"
          window = "hour"
          value  = 2000000
        },
      ]
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = ""
  }]
}
`

const gatewayPolicyDefaultRateLimitConfigUpdated = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-default-rate-limit-updated"
  description = "updated by TestAccGatewayPolicyRateLimit"
  action      = "block"
  enabled     = false

  config = {
    default_rate_limit = {
      version = 1
      limits = [
        {
          metric = "requests"
          window = "hour"
          value  = 1500
        },
        {
          metric = "tokens"
          window = "hour"
          value  = 2000000
        },
        {
          metric = "requests"
          window = "minute"
          value  = 100
        },
      ]
    }
  }

  subject_matchers = [
    {
      key   = "workspace_id"
      value = ""
    },
  ]
}
`

// TestAccGatewayPolicyRateLimit exercises Terraform lifecycle.
func TestAccGatewayPolicyRateLimit(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run rate_limit gateway policy smoke test")
	}

	tests := []struct {
		policy_type string
		isDefault   bool
		original    string
		update      string
	}{
		{
			policy_type: "rate_limit",
			isDefault:   false,
			original:    gatewayPolicyRateLimitConfig,
			update:      gatewayPolicyRateLimitConfigUpdated,
		},
		{
			policy_type: "default_rate_limit",
			isDefault:   true,
			original:    gatewayPolicyDefaultRateLimitConfig,
			update:      gatewayPolicyDefaultRateLimitConfigUpdated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.policy_type, func(t *testing.T) {
			var policyID string
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"langsmith": providerserver.NewProtocol6WithError(New("test")()),
				},
				Steps: []resource.TestStep{
					// create
					{
						Config: tt.original,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
								if value == "" {
									return fmt.Errorf("id is empty")
								}
								policyID = value
								return nil
							}),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "created by TestAccGatewayPolicyRateLimit"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "policy_type", tt.policy_type),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "true"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "priority", "0"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".version", "1"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.#", "2"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.0.metric", "requests"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.0.window", "minute"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.0.value", "25"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.1.metric", "tokens"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.1.window", "hour"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.1.value", "2000000"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.#", "1"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.key", "workspace_id"),
							func() resource.TestCheckFunc {
								targetValue := "00000000-0000-4000-8000-000000000005"
								if tt.isDefault {
									targetValue = ""
								}
								return resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.value", targetValue)
							}(),
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
						Config: tt.update,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
								if value != policyID {
									return fmt.Errorf("id = %q, want %q", value, policyID)
								}
								return nil
							}),
							func() resource.TestCheckFunc {
								name := "tf-provider-gateway-policy-rate-limit-updated"
								if tt.isDefault {
									name = "tf-provider-gateway-policy-default-rate-limit-updated"
								}
								return resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "name", name)
							}(),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "updated by TestAccGatewayPolicyRateLimit"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "false"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.#", "3"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.0.metric", "requests"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.0.window", "hour"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.0.value", "1500"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.1.metric", "tokens"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.1.window", "hour"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.1.value", "2000000"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.2.metric", "requests"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.2.window", "minute"),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config."+tt.policy_type+".limits.2.value", "100"),
							func() resource.TestCheckFunc {
								count := "2"
								if tt.isDefault {
									count = "1"
								}
								return resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.#", count)
							}(),
							resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.key", "workspace_id"),
							func() resource.TestCheckFunc {
								targetValue := "00000000-0000-4000-8000-000000000005"
								if tt.isDefault {
									targetValue = ""
								}
								return resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.value", targetValue)
							}(),
							// default_rate_limit's updated config only has one matcher, since a second
							// {key: "workspace_id", value: ""} would be a duplicate list value.
							func() resource.TestCheckFunc {
								if tt.isDefault {
									return resource.ComposeAggregateTestCheckFunc()
								}
								return resource.ComposeAggregateTestCheckFunc(
									resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.1.key", "workspace_id"),
									resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.1.value", "00000000-0000-4000-8000-000000000006"),
								)
							}(),
						),
					},
					// Delete testing automatically occurs in TestCase
					// The API client will error if delete fails, and the Delete() method will add an error to the response,
					// failing the test.
				},
			})
		})
	}
}

const gatewayPolicyGuardConfig = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-guard"
  description = "created by TestAccGatewayPolicyGuard"
  action      = "block"
  # omit enabled/priority to exercise schema defaults

  config = {
    guard = {
      version = 1
      detect = {
        pii     = { enabled = true }
        secrets = true
      }
      timeout_seconds = 3
      timeout_action  = "allow"
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = "00000000-0000-4000-8000-00000000000a"
  }]
}
`

const gatewayPolicyGuardConfigUpdated = `
resource "langsmith_gateway_policy" "test" {
  name        = "tf-provider-gateway-policy-guard-updated"
  description = "updated by TestAccGatewayPolicyGuard"
  action      = "block"
  enabled     = false

  config = {
    guard = {
      version = 1
      detect = {
        pii     = { rules = [{ id = "email-address" }, { id = "us-ssn" }] }
        secrets = false
      }
      trace = {
        capture_content = false
      }
      timeout_action = "block"
    }
  }

  subject_matchers = [
    {
      key   = "workspace_id"
      value = "00000000-0000-4000-8000-00000000000a"
    },
    {
      key   = "workspace_id"
      value = "00000000-0000-4000-8000-00000000000b"
    },
  ]
}
`

// TestAccGatewayPolicyGuard exercises Terraform lifecycle.
func TestAccGatewayPolicyGuard(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run guard gateway policy smoke test")
	}

	var policyID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			// create
			{
				Config: gatewayPolicyGuardConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
						if value == "" {
							return fmt.Errorf("id is empty")
						}
						policyID = value
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "created by TestAccGatewayPolicyGuard"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "policy_type", "guard"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "true"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "priority", "0"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.version", "1"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.pii.enabled", "true"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.pii.rules"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.secrets", "true"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.timeout_seconds", "3"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.timeout_action", "allow"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "config.guard.trace"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.#", "1"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.key", "workspace_id"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.value", "00000000-0000-4000-8000-00000000000a"),
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
				Config: gatewayPolicyGuardConfigUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("langsmith_gateway_policy.test", "id", func(value string) error {
						if value != policyID {
							return fmt.Errorf("id = %q, want %q", value, policyID)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "name", "tf-provider-gateway-policy-guard-updated"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "description", "updated by TestAccGatewayPolicyGuard"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "enabled", "false"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.pii.enabled"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.pii.rules.#", "2"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.pii.rules.0.id", "email-address"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.pii.rules.1.id", "us-ssn"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.detect.secrets", "false"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.trace.capture_content", "false"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "config.guard.timeout_action", "block"),
					resource.TestCheckNoResourceAttr("langsmith_gateway_policy.test", "config.guard.timeout_seconds"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.#", "2"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.key", "workspace_id"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.0.value", "00000000-0000-4000-8000-00000000000a"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.1.key", "workspace_id"),
					resource.TestCheckResourceAttr("langsmith_gateway_policy.test", "subject_matchers.1.value", "00000000-0000-4000-8000-00000000000b"),
				),
			},
			// Delete testing automatically occurs in TestCase
			// The API client will error if delete fails, and the Delete() method will add an error to the response,
			// failing the test.
		},
	})
}
