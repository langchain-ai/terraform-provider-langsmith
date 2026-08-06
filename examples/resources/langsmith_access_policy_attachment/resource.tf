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
}

resource "langsmith_access_policy_attachment" "production_reader" {
  role_id          = langsmith_workspace_role.production_reader.id
  access_policy_id = langsmith_access_policy.production_readers.id
}
