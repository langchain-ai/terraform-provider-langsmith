# Register a container-image registry that sandboxes pull images from.
# Credentials are write-only: the API never returns them, so they live in
# (sensitive) Terraform state — use encrypted remote state.
resource "langsmith_sandbox_registry" "docker_hub" {
  name     = "docker-hub"
  url      = "https://index.docker.io/v1/"
  username = var.registry_username
  password = var.registry_password
}
