# Create an organization-scoped LangSmith service key for CI/automation use.
resource "langsmith_service_key" "ci" {
  description = "CI / Terraform automation"

  # Optionally scope the key to specific workspaces and assign a role.
  # workspaces = [langsmith_workspace.ci.id]
  # role_id    = data.langsmith_workspace_role.admin.id
}

# The secret (`key`) is returned by LangSmith only once, at creation, and is
# stored in (sensitive) Terraform state. Write it straight into a secret manager
# rather than exposing it as a root-level output.
resource "aws_secretsmanager_secret" "langsmith" {
  name = "langsmith/ci-service-key"
}

resource "aws_secretsmanager_secret_version" "langsmith" {
  secret_id     = aws_secretsmanager_secret.langsmith.id
  secret_string = langsmith_service_key.ci.key
}
