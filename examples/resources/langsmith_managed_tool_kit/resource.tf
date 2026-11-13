resource "langsmith_managed_tool_kit" "agent" {
  name        = "Support agent"
  description = "Tools for the support agent."

  tools = [
    {
      server_id = langsmith_managed_tool_server.api_key.id
      tool_name = "search_tickets"
    },
    {
      server_id    = langsmith_managed_tool_server.api_key.id
      tool_name    = "get_ticket"
      exposed_name = "Get ticket"
    },
  ]
}

output "kit_mcp_path" {
  value = langsmith_managed_tool_kit.agent.mcp_path
}
