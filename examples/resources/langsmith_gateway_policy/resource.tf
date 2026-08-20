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

resource "langsmith_gateway_policy" "hourly_rate_limit" {
  name        = "hourly-rate-limit"
  description = "Block requests once a workspace exceeds the per-minute request limit or hourly token limit."
  action      = "block"

  config = {
    rate_limit = {
      version = 1
      limits = {
        requests = {
          window = "minute"
          value  = 25
        }
        tokens = {
          window = "hour"
          value  = 2000000
        }
      }
    }
  }

  subject_matchers = [{
    key   = "workspace_id"
    value = "00000000-0000-0000-0000-000000000000"
  }]
}
