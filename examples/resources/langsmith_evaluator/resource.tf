resource "langsmith_evaluator" "example" {
  workspace_id = "11111111-1111-1111-1111-111111111111"
  name         = "tool call counts"
  type         = "code"

  code_evaluator = {
    language = "javascript"
    code     = <<-EOT
      function performEval(run) {
        const outputs = (run && run.outputs) || {}
        return { ok: outputs.error ? 0 : 1 }
      }
    EOT
  }
}
