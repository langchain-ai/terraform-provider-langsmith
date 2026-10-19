resource "langsmith_agent" "support" {
  agent_id    = "support-agent"
  name        = "Support Agent"
  source_type = "EXTERNAL"
  agent_type  = "CUSTOM"

  lifecycle {
    prevent_destroy = true
  }
}

resource "langsmith_agent" "development" {
  workspace_id = "00000000-0000-0000-0000-000000000001"
  agent_id     = "development-agent"
  source_type  = "EXTERNAL"
}

output "production_project_id" {
  value = langsmith_agent.support.environments["PRODUCTION"].tracer_session_id
}
