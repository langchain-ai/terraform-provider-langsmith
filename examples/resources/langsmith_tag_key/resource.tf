resource "langsmith_tag_key" "environment" {
  workspace_id = "00000000-0000-0000-0000-000000000000"
  key          = "Environment"
  description  = "Deployment environment"
}
