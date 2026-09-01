data "langsmith_workspace_secrets" "current" {}

# Ensure no typos in model configuration `env_var_name`.
resource "langsmith_model_configuration" "openai_gpt4o" {
  name           = "gpt-4o (default)"
  model_provider = "openai"
  model          = "gpt-4o"
  env_var_name   = "OPENAI_API_KEY"

  lifecycle {
    precondition {
      condition     = contains(data.langsmith_workspace_secrets.current.keys, "OPENAI_API_KEY")
      error_message = "Workspace secret OPENAI_API_KEY is not set."
    }
  }
}
