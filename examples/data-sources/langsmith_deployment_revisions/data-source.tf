data "langsmith_deployment_revisions" "example" {
  deployment_id = "11111111-1111-1111-1111-111111111111"
  limit         = 20
  offset        = 0
  status        = "DEPLOYED,DEPLOY_FAILED"
}
