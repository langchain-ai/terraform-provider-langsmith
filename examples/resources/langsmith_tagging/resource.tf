resource "langsmith_tag" "production" {
  key   = "Environment"
  value = "production"
}

resource "langsmith_tagging" "production_project" {
  tag_value_id  = langsmith_tag.production.tag_value_id
  resource_type = "project"
  resource_id   = "00000000-0000-0000-0000-000000000000"
}
