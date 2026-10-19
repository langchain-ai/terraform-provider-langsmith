data "langsmith_agent" "support" {
  agent_id = "support-agent"
}

data "langsmith_agent" "development" {
  workspace_id = "00000000-0000-0000-0000-000000000001"
  agent_id     = "development-agent"
}

output "production_environment_id" {
  value = data.langsmith_agent.support.environments["PRODUCTION"].id
}
