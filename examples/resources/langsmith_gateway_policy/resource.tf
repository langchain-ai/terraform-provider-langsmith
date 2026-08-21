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

resource "langsmith_gateway_policy" "burst_and_sustained_rate_limit" {
  name        = "burst-and-sustained-rate-limit"
  description = "Block requests once a API key exceeds request or token limits"
  action      = "block"

  config = {
    rate_limit = {
      version = 1
      limits = [
        { # optional: caps requests within a minute
          metric = "requests"
          window = "minute"
          value  = 25
        },
        { # optional: caps request volume over an hour
          metric = "requests"
          window = "hour"
          value  = 1000
        },
        { # optional: caps tokens within a minute
          metric = "tokens"
          window = "minute"
          value  = 50000
        },
        { # optional: caps token volume over an hour
          metric = "tokens"
          window = "hour"
          value  = 2000000
        },
      ]
    }
  }

  subject_matchers = [{
    key   = "api_key_id"
    value = "00000000-0000-0000-0000-000000000000"
  }]
}
