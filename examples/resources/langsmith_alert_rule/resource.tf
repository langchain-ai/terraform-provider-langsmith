resource "langsmith_alert_rule" "example" {
  session_id     = "00000000-0000-0000-0000-000000000000" # project (session) ID
  name           = "run error count high"
  description    = "Sum of run errors exceeded the threshold in the last 15 minutes."
  type           = "threshold"
  attribute      = "error_count"
  aggregation    = "sum"
  window_minutes = 15
  operator       = "gte"
  threshold      = 10
  filter         = "eq(is_root, true)"

  actions = [{
    target  = "webhook"
    url_env = "LANGSMITH_ALERTS_WEBHOOK_URL"
    config_json = jsonencode({
      project_name = "my-project"
      headers      = jsonencode({ "Content-Type" = "application/json" })
      body         = jsonencode({ text = "Error rate elevated for my-project" })
    })
  }]
}
