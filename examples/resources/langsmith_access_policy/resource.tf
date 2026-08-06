resource "langsmith_access_policy" "production_readers" {
  name        = "Production readers"
  description = "Read access to production projects"
  effect      = "allow"

  condition_groups = [{
    permission    = "projects:read"
    resource_type = "project"
    conditions = [{
      attribute_name  = "resource_tag_key"
      attribute_key   = "Environment"
      operator        = "equals"
      attribute_value = "production"
    }]
  }]
}
