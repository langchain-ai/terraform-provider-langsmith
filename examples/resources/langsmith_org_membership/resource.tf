data "langsmith_org_role" "user" {
  name = "ORGANIZATION_USER"
}

resource "langsmith_org_membership" "example" {
  email   = "alice@example.com"
  role_id = data.langsmith_org_role.user.id
}
