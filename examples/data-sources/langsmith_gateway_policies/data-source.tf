# Every spend cap in the organization, including the ones the gateway
# materialized for individual subjects from a default.
data "langsmith_gateway_policies" "spend_caps" {
  policy_type = "spend_cap"
}

# Caps the gateway materialized, which no one declared and Terraform does not
# manage. Editing one detaches it from its default for good.
output "materialized_caps" {
  value = [
    for policy in data.langsmith_gateway_policies.spend_caps.policies : {
      id           = policy.id
      subject      = one(policy.subject_matchers).value
      limit_usd    = jsondecode(policy.config_json).limit_usd
      spent_usd    = policy.current_spend_usd
      from_default = policy.parent_policy_id
    }
    if policy.parent_policy_id != null
  ]
}

# Whatever applies to one workspace, of any policy type.
data "langsmith_gateway_policies" "workspace" {
  subject_matcher_key   = "workspace_id"
  subject_matcher_value = "11111111-1111-1111-1111-111111111111"
}
