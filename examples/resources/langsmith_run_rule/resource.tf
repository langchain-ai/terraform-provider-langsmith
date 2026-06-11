resource "langsmith_run_rule" "example" {
  workspace_id  = "11111111-1111-1111-1111-111111111111"
  display_name  = "tool call counts"
  session_id    = "00000000-0000-0000-0000-000000000000" # project (session) ID
  sampling_rate = 1
  filter        = "eq(is_root, true)"

  evaluator_id = "22222222-2222-2222-2222-222222222222" # langsmith_evaluator.<name>.id
}
