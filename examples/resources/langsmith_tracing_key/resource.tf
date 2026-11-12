# The provider uses a service key or PAT with creation and management permissions.
# Each resource has a fixed workspace scope; protect Terraform state containing key.
resource "langsmith_tracing_key" "production" {
  description = "Production tracing"
  workspaces  = ["11111111-1111-1111-1111-111111111111"]
  expires_at  = "2030-01-01T00:00:00Z"
}
