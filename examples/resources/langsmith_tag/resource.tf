resource "langsmith_tag" "production" {
  workspace_id      = "00000000-0000-0000-0000-000000000000"
  key               = "Environment"
  value             = "production"
  key_description   = "Deployment environment"
  value_description = "Production workloads"
}
