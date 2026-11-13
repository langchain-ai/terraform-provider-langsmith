resource "langsmith_oauth_connection" "example" {
  workspace_id               = var.workspace_id
  slug                       = "example-oauth"
  display_name               = "Example OAuth"
  client_id                  = var.oauth_client_id
  client_secret_env          = "EXAMPLE_OAUTH_CLIENT_SECRET"
  client_secret_version      = "1"
  authorization_url          = "https://identity.example.com/oauth/authorize"
  token_url                  = "https://identity.example.com/oauth/token"
  token_endpoint_auth_method = "client_secret_basic"
  default_scopes             = ["tools:read"]
  allowed_scopes             = ["tools:read", "tools:write"]
}

output "connection_slug" {
  value = langsmith_oauth_connection.example.slug
}

output "oauth_provider_id" {
  value = langsmith_oauth_connection.example.oauth_provider_id
}
