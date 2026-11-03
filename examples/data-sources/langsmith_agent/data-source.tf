data "langsmith_agent" "example" {
  agent_id = "example"

  # Omit to use the workspace configured on the provider.
  workspace_id = "00000000-0000-0000-0000-000000000000"
}
