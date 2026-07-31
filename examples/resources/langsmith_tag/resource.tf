resource "langsmith_tag" "production" {
  key               = "Environment"
  value             = "production"
  key_description   = "Deployment environment"
  value_description = "Production workloads"
}
