# A run rule (LangSmith "automation") acts on runs matching `filter` in a tracing
# project (`session_id`) or dataset (`dataset_id`) — set exactly one. Each rule can
# attach evaluators, curate a dataset, route to an annotation queue, and/or call
# webhooks. The examples below show the common rule types.

# Attach a saved evaluator to score matching runs.
resource "langsmith_run_rule" "score" {
  workspace_id  = "11111111-1111-1111-1111-111111111111"
  display_name  = "score root runs"
  session_id    = "00000000-0000-0000-0000-000000000000" # tracing project ID
  sampling_rate = 1
  filter        = "eq(is_root, true)"

  evaluator_id = "22222222-2222-2222-2222-222222222222" # langsmith_evaluator.<name>.id
}

# Run an inline code evaluator without a saved langsmith_evaluator resource.
resource "langsmith_run_rule" "inline_code" {
  workspace_id  = "11111111-1111-1111-1111-111111111111"
  display_name  = "inline tool-call counter"
  session_id    = "00000000-0000-0000-0000-000000000000"
  sampling_rate = 1
  filter        = "eq(is_root, true)"

  code_evaluators_json = jsonencode([{
    language = "javascript"
    code     = "function performEval(run) { return { ok: run.error ? 0 : 1 } }"
  }])
}

# Curate a dataset from matching runs.
resource "langsmith_run_rule" "to_dataset" {
  workspace_id  = "11111111-1111-1111-1111-111111111111"
  display_name  = "collect llm runs"
  session_id    = "00000000-0000-0000-0000-000000000000"
  sampling_rate = 1
  filter        = "eq(run_type, \"llm\")"

  add_to_dataset_id = "33333333-3333-3333-3333-333333333333"
}

# Push matching runs into an annotation queue for human review.
resource "langsmith_run_rule" "to_queue" {
  workspace_id  = "11111111-1111-1111-1111-111111111111"
  display_name  = "queue root runs for review"
  session_id    = "00000000-0000-0000-0000-000000000000"
  sampling_rate = 1
  filter        = "eq(is_root, true)"

  add_to_annotation_queue_id = "44444444-4444-4444-4444-444444444444"
}

# Call a webhook when a rule matches. Use url_env to keep the URL out of state.
resource "langsmith_run_rule" "notify" {
  workspace_id  = "11111111-1111-1111-1111-111111111111"
  display_name  = "notify on root runs"
  session_id    = "00000000-0000-0000-0000-000000000000"
  sampling_rate = 1
  filter        = "eq(is_root, true)"

  webhooks = [{
    url_env      = "RUN_RULE_WEBHOOK_URL"
    headers_json = jsonencode({ "Content-Type" = "application/json" })
  }]
}
