data "langsmith_project" "api" {
  name = "my-api"
}

resource "langsmith_dashboard" "service_health" {
  title       = "Service health"
  description = "Managed by Terraform"

  charts = [
    {
      key   = "requests"
      type  = "line"
      title = "Requests"
      series = [{
        name        = "root runs"
        metric      = { type = "count" }
        project_ids = [data.langsmith_project.api.id]
        run_filter  = "eq(is_root, true)"
      }]
    },
    {
      key   = "latency"
      type  = "line"
      title = "p99 latency"
      series = [{
        name        = "p99"
        metric      = { type = "percentile", field = "latency_seconds", percentile = 0.99 }
        project_ids = [data.langsmith_project.api.id]
        group_by    = [{ attribute = "metadata", path = "env" }]
      }]
    },
    {
      key   = "error_rate"
      type  = "kpi"
      title = "Error rate"
      series = [{
        name = "errors / runs"
        metric = {
          type        = "ratio"
          numerator   = { type = "count", filter = "eq(status, \"error\")" }
          denominator = { type = "count" }
        }
        project_ids   = [data.langsmith_project.api.id]
        metadata_json = jsonencode({ value_format = "percent" })
      }]
    },
    {
      key      = "runbook"
      type     = "text"
      markdown = "Paged? See the [runbook](https://example.com/runbook)."
    },
  ]

  # Charts share a row evenly unless every chart in the row sets width_units
  # (out of 120). Charts left out of the layout are placed after it.
  layout = [
    { height_units = 4, charts = [{ key = "requests" }, { key = "latency" }] },
    { height_units = 3, charts = [{ key = "error_rate", width_units = 40 }, { key = "runbook", width_units = 80 }] },
  ]
}
