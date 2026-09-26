# Configure the provider with your SaaS control-plane endpoint and organization
# administrator credentials. The organization must have BYOC enabled, and the
# role must trust the organization's assigned external ID.
resource "langsmith_data_plane" "example" {
  name     = "production"
  region   = "us-east-1"
  role_arn = "arn:aws:iam::123456789012:role/langsmith-byoc"
  vpc_cidr = "10.20.0.0/16"

  byoiam_enabled               = false
  public_load_balancer         = false
  eks_api_privatelink_disabled = false
  additional_tags              = { Environment = "production" }

  maintenance_window = "sun:03:00-sun:05:00"
  ttl = {
    enabled    = true
    short_days = 14
    long_days  = 400
  }
  firewall = {
    allowed_domains = [".aws.langchain-byoc.com", "api.example.com"]
    allow_http      = false
    allowed_cidrs   = { "10.0.0.0/8" = [443] }
  }
  fleet_oidc = {
    is_enabled      = false
    provider        = "custom"
    issuer_url      = "https://identity.example.com"
    audience        = "langsmith-fleet"
    subject_claim   = "sub"
    tenant_claim    = "tenant"
    tenant_mappings = {}
    email_claim     = "email"
    groups_claim    = "groups"
  }

  timeouts = {
    create = "2h"
    update = "1h"
    delete = "2h"
  }

  lifecycle {
    prevent_destroy = true
  }
}

# Alternatively, replace vpc_cidr with a customer-managed VPC configuration:
# byovpc = {
#   vpc_id                 = "vpc-0123456789abcdef0"
#   public_subnet_ids      = ["subnet-11111111", "subnet-22222222"]
#   private_app_subnet_ids = ["subnet-33333333", "subnet-44444444"]
#   private_db_subnet_ids  = ["subnet-55555555", "subnet-66666666"]
# }

output "data_plane_status" {
  value = langsmith_data_plane.example.status
}

output "data_plane_api_url" {
  value = langsmith_data_plane.example.api_url
}

output "data_plane_workspaces" {
  value = langsmith_data_plane.example.workspaces
}
