# Clone a built-in role's permissions into a custom workspace role.
data "langsmith_workspace_role" "admin" {
  name = "WORKSPACE_ADMIN"
}

resource "langsmith_workspace_role" "example" {
  display_name = "Issues Agent (Replica)"
  description  = data.langsmith_workspace_role.admin.description
  permissions  = data.langsmith_workspace_role.admin.permissions
}
