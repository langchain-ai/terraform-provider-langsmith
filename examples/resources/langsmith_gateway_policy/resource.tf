resource "langsmith_gateway_policy" "monthly_spend_cap" {
  name        = "monthly-spend-cap"
  description = "Block requests once workspace monthly spend exceeds the limit."
  action      = "block"

  config = {
    spend_cap = {
      window    = "monthly"
      limit_usd = 100
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = "00000000-0000-0000-0000-000000000000"
  }]
}
