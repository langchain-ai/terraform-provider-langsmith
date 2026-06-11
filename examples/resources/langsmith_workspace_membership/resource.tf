data "langsmith_workspace_role" "admin" {
  name = "WORKSPACE_ADMIN"
}

resource "langsmith_workspace_membership" "example" {
  workspace_id = "11111111-1111-1111-1111-111111111111"
  email        = "alice@example.com"
  role_id      = data.langsmith_workspace_role.admin.id
}
