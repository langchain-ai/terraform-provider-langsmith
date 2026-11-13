variable "api_key_mcp_token" {
  type      = string
  sensitive = true
}

resource "langsmith_managed_tool_profile_credential" "agent" {
  profile_id = langsmith_managed_tool_profile.agent.id
  server_id  = langsmith_managed_tool_server.api_key.id
  fields = {
    Authorization = "Bearer ${var.api_key_mcp_token}"
  }
}
