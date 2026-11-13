terraform {
  required_providers {
    langsmith = {
      source = "langchain-ai/langsmith"
    }
  }
}

provider "langsmith" {}

variable "gong_client_id" {
  type        = string
  description = "Client ID from the manual MCP integration registered in Gong."
}

variable "gong_token_endpoint_auth_method" {
  type        = string
  description = "Client authentication method required by your Gong integration: client_secret_basic or client_secret_post."
  validation {
    condition     = contains(["client_secret_basic", "client_secret_post"], var.gong_token_endpoint_auth_method)
    error_message = "Select the client authentication method required by Gong."
  }
}

resource "langsmith_oauth_connection" "gong" {
  slug                       = "gong"
  display_name               = "Gong"
  client_id                  = var.gong_client_id
  client_secret_env          = "GONG_CLIENT_SECRET"
  authorization_url          = "https://app.gong.io/oauth2/authorize"
  token_url                  = "https://app.gong.io/oauth2/generate-mcp-token"
  token_endpoint_auth_method = var.gong_token_endpoint_auth_method
  default_scopes             = ["mcp:read"]
  allowed_scopes             = ["mcp:read"]
}

resource "langsmith_managed_tool_server" "gong" {
  name                       = "Gong"
  upstream_url               = "https://mcp.gong.io/mcp"
  connection_type            = "oauth"
  agent_auth_connection_slug = langsmith_oauth_connection.gong.slug
}

output "gong_mcp_url" {
  value = langsmith_managed_tool_server.gong.url
}
