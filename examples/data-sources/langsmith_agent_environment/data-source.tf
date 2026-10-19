data "langsmith_agent" "support" {
  agent_id = "support-agent"
}

data "langsmith_agent_environment" "production" {
  id = data.langsmith_agent.support.environments["PRODUCTION"].id
}

data "langsmith_agent_environment" "development" {
  workspace_id = "00000000-0000-0000-0000-000000000001"
  id           = "00000000-0000-0000-0000-000000000002"
}

output "production_project_id" {
  value = data.langsmith_agent_environment.production.tracer_session_id
}
