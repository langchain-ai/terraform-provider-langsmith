resource "langsmith_tag_key" "environment" {
  key = "Environment"
}

resource "langsmith_tag_value" "production" {
  tag_key_id = langsmith_tag_key.environment.id
  value      = "production"
}
