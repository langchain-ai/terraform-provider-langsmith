resource "langsmith_tag" "production" {
  workspace_id = "00000000-0000-0000-0000-000000000000"
  key          = "Environment"
  value        = "production"
}

resource "langsmith_tagging" "production_project" {
  workspace_id  = langsmith_tag.production.workspace_id
  tag_value_id  = langsmith_tag.production.tag_value_id
  resource_type = "project"
  resource_id   = "00000000-0000-0000-0000-000000000000"
}
