resource "langsmith_managed_tool_server" "public" {
  name         = "public-mcp"
  upstream_url = "https://mcp.example.com/mcp"
}

resource "langsmith_managed_tool_server" "oauth" {
  name                       = "oauth-mcp"
  upstream_url               = "https://oauth-mcp.example.com/mcp"
  connection_type            = "oauth"
  agent_auth_connection_slug = "existing-oauth-connection"
  workspace_id               = "00000000-0000-0000-0000-000000000001"
}

output "managed_mcp_url" {
  value = langsmith_managed_tool_server.oauth.url
}

output "oauth_setup" {
  value = langsmith_managed_tool_server.oauth.oauth_setup
}

resource "langsmith_managed_tool_server" "api_key" {
  name            = "api-key-mcp"
  upstream_url    = "https://api-key-mcp.example.com/mcp"
  connection_type = "secret"
  secret_headers  = ["Authorization"]
}
