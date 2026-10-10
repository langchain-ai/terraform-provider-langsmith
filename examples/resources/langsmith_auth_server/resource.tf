resource "langsmith_auth_server" "example" {
  workspace_id               = "your-workspace-id"
  slug                       = "example-oauth"
  display_name               = "Example OAuth"
  client_id                  = "your-client-id"
  client_secret              = var.oauth_client_secret
  authorization_url          = "https://auth.example.com/authorize"
  token_url                  = "https://auth.example.com/token"
  token_endpoint_auth_method = "client_secret_post"
  grant_type                 = "authorization_code"
  allowed_scopes             = ["read"]
  default_scopes             = ["read"]
}

variable "oauth_client_secret" {
  type      = string
  sensitive = true
}
