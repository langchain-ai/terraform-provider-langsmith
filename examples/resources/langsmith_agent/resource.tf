resource "langsmith_agent" "example" {
  agent_id    = "example"
  name        = "Example Agent"
  source_type = "EXTERNAL"

  # Omit to use the workspace configured on the provider.
  workspace_id = "00000000-0000-0000-0000-000000000000"
}

output "production_tracing_project_id" {
  value = langsmith_agent.example.environments["PRODUCTION"].tracer_session_id
}
