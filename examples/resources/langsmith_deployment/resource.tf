resource "langsmith_deployment" "agent" {
  name         = "support-agent"
  display_name = "Support Agent"
  source       = "github"

  source_config = {
    integration_id  = var.github_integration_id
    repo_url        = "https://github.com/example/support-agent"
    deployment_type = "dev"
    build_on_push    = true
  }

  source_revision_config = {
    repo_ref              = "main"
    langgraph_config_path = "langgraph.json"
  }

  secrets = {
    OPENAI_API_KEY = var.openai_api_key
  }
  secrets_version = "1"

  shareable             = false
  route_through_gateway = true
}
