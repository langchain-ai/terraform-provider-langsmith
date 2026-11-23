# A relay sends sandbox traffic for chosen hosts out through a connector on
# your network. Run the connector with `langsmith relay connect <id>`, and put
# the ID in sandbox proxy relay rules (`relay_id`).
resource "langsmith_relay" "ci_tailnet" {
  name        = "ci-tailnet"
  description = "CI sandbox egress to tailnet services"
}

output "ci_tailnet_relay_id" {
  value = langsmith_relay.ci_tailnet.id
}
