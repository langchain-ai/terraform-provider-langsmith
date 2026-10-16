resource "langsmith_workspace" "example" {
  display_name  = "Demo Workspace"
  tenant_handle = "demo-workspace"
}

# The provider endpoint must be able to resolve the data plane in your organization.
# Creation goes through the selected data plane, which proxies to the control plane.
resource "langsmith_workspace" "byoc" {
  display_name  = "BYOC Workspace"
  data_plane_id = "00000000-0000-0000-0000-000000000001"
}
