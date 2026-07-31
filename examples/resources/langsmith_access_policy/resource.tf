resource "langsmith_workspace_role" "production_reader" {
  display_name = "Production Reader"
  description  = "Can read production projects"
  permissions  = ["projects:read"]
}

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

  role_ids = [langsmith_workspace_role.production_reader.id]
}
