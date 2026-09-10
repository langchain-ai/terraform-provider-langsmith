variable "environment" {
  type      = map(string)
  sensitive = true
  ephemeral = true
}

resource "langsmith_deployment" "agent" {
  name         = "support-agent"
  display_name = "Support Agent"
  source       = "github"

  source_config = {
    integration_id  = var.github_integration_id
    repo_url        = "https://github.com/example/support-agent"
    deployment_type = "dev"
    build_on_push   = true
  }

  source_revision_config = {
    repo_ref              = "main"
    langgraph_config_path = "langgraph.json"
  }

  # Supply the complete environment; updates replace the existing map.
  # The provider compares a digest and keeps plaintext out of state.
  secrets = var.environment
}
