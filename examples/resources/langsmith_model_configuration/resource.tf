resource "langsmith_model_configuration" "openai_gpt4o" {
  name           = "gpt-4o (default)"
  model_provider = "openai"
  model          = "gpt-4o"
  env_var_name   = "OPENAI_API_KEY"

  invocation_params = jsonencode({
    temperature = 0.7
  })
}

resource "langsmith_model_configuration" "azure_gpt4o" {
  name           = "gpt-4o (azure)"
  model_provider = "azure_openai"
  model          = "gpt-4o"
  env_var_name   = "AZURE_OPENAI_API_KEY"
  base_url       = "https://my-resource.openai.azure.com"

  invocation_params = jsonencode({
    deployment_name    = "my-gpt4o-deployment"
    openai_api_version = "2024-02-15-preview"
  })
}

resource "langsmith_model_configuration" "anthropic_claude" {
  name           = "claude-sonnet (default)"
  model_provider = "anthropic"
  model          = "claude-sonnet-5"
  env_var_name   = "ANTHROPIC_API_KEY"

  invocation_params = jsonencode({
    temperature = 0.7
  })
}

resource "langsmith_model_configuration" "org_openai_gpt4o" {
  name           = "gpt-4o (org default)"
  model_provider = "openai"
  model          = "gpt-4o"
  env_var_name   = "OPENAI_API_KEY"
  scope          = "organization"
}
