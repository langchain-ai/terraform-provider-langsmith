# Organization-wide key with API default roles (Workspace Admin + Organization User).
resource "langsmith_service_key" "org_wide" {
  description = "CI / automation org-wide key"
}

# Organization-wide key with explicit roles.
data "langsmith_workspace_role" "viewer" {
  name = "WORKSPACE_VIEWER"
}

data "langsmith_org_role" "viewer" {
  name = "ORGANIZATION_VIEWER"
}

resource "langsmith_service_key" "org_wide_custom_roles" {
  description = "org-wide key with viewer roles"
  role_id     = data.langsmith_workspace_role.viewer.id
  org_role_id = data.langsmith_org_role.viewer.id
}

# Workspace-scoped key (org_role_id cannot be set with workspaces).
resource "langsmith_service_key" "workspace" {
  description = "workspace-scoped key"
  workspaces  = ["11111111-1111-1111-1111-111111111111"]
  role_id     = data.langsmith_workspace_role.viewer.id
}
