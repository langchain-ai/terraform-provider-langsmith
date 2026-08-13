resource "langsmith_gateway_spend_cap" "workspace_monthly" {
  name        = "workspace monthly"
  description = "Cap production workspace spend"
  window      = "monthly"
  limit_usd   = 100

  subject_matchers = [{
    key   = "workspace_id"
    value = "11111111-1111-1111-1111-111111111111"
  }]
}
