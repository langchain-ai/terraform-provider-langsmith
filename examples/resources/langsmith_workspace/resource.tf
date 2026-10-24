resource "langsmith_workspace" "example" {
  display_name    = "Demo Workspace"
  tenant_handle   = "demo-workspace"
  deletion_policy = "PREVENT"
}
