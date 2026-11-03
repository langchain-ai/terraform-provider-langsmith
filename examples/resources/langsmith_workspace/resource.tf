resource "langsmith_workspace" "example" {
  display_name  = "Demo Workspace"
  tenant_handle = "demo-workspace"
}

# Create a workspace on a BYOC data plane. The provider sends the request
# through that data plane's API URL.
resource "langsmith_workspace" "byoc" {
  display_name  = "BYOC Workspace"
  data_plane_id = "00000000-0000-0000-0000-000000000000"
}
