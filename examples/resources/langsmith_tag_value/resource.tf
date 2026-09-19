resource "langsmith_tag_key" "environment" {
  workspace_id = "00000000-0000-0000-0000-000000000000"
  key          = "Environment"
}

resource "langsmith_tag_value" "production" {
  workspace_id = langsmith_tag_key.environment.workspace_id
  tag_key_id   = langsmith_tag_key.environment.id
  value        = "production"
}
