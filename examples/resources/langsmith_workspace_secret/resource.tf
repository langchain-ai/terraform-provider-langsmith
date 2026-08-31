resource "langsmith_workspace_secret" "openai" {
  key   = "OPENAI_API_KEY"
  value = var.openai_api_key
}

# Model configurations reference a secret by name.
resource "langsmith_model_configuration" "openai_gpt4o" {
  name           = "gpt-4o (default)"
  model_provider = "openai"
  model          = "gpt-4o"
  env_var_name   = langsmith_workspace_secret.openai.key
}

# Adopt a secret that already exists. A plain create is refused for a key that
# is already present. The API cannot read the current value back, so the apply
# overwrites it with the value below.
import {
  to = langsmith_workspace_secret.anthropic
  id = "ANTHROPIC_API_KEY"
}

resource "langsmith_workspace_secret" "anthropic" {
  key   = "ANTHROPIC_API_KEY"
  value = var.anthropic_api_key
}
