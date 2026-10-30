# dashboard.json holds a definition exported from LangSmith with
# GET /api/v1/charts/section/{dashboard_id}/definition.
resource "langsmith_dashboard_json" "from_ui" {
  dashboard = file("${path.module}/dashboard.json")
}

# Or write the definition inline.
resource "langsmith_dashboard_json" "inline" {
  dashboard = jsonencode({
    title = "Runs"
    charts = [{
      key        = "runs"
      title      = "Runs"
      chart_type = "bar"
      series = [{
        name              = "runs"
        metric_definition = { type = "count" }
        filter_definition = {
          source_type = "tracing_project"
          project_ids = ["00000000-0000-0000-0000-000000000000"]
        }
      }]
    }]
  })
}
