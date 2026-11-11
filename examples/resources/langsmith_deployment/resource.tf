variable "deployment_secrets" {
  type      = map(string)
  sensitive = true
  ephemeral = true
}

resource "langsmith_agent" "example" {
  workspace_id = "00000000-0000-0000-0000-000000000000"
  agent_id     = "example"
  source_type  = "LSD"
}

resource "langsmith_deployment" "agent" {
  workspace_id = "00000000-0000-0000-0000-000000000000"
  agent = {
    agent_id    = langsmith_agent.example.id
    environment = "staging"
  }
  display_name = "Example Agent"
  source       = "github"

  source_config = {
    integration_id  = var.github_integration_id
    repo_url        = "https://github.com/example/agent"
    deployment_type = "dev"
    build_on_push   = true
  }

  source_revision_config = {
    repo_ref              = "main"
    langgraph_config_path = "langgraph.json"
  }

  # Ordinary values appear in Terraform plans and state.
  environment_variables = {
    LOG_LEVEL = "info"
  }

  # Both maps together replace the complete API environment.
  # Secret values remain ephemeral and write-only.
  secrets = var.deployment_secrets
}
