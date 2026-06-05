terraform {
  required_providers {
    langsmith = {
      source = "langchain-ai/langsmith"
    }
  }
}

provider "langsmith" {
  profile = "local"
}

data "langsmith_info" "current" {}

data "langsmith_project" "default" {
  name = "default"
}

resource "langsmith_evaluator" "correctness" {
  name = "correctness"
  type = "llm"

  llm_evaluator {
    prompt_repo_handle = "langchain-ai/correctness"
    commit_hash_or_tag = "latest"

    variable_mapping_json = jsonencode({
      question = "inputs.question"
      answer   = "outputs.answer"
    })
  }
}

resource "langsmith_run_rule" "online_correctness" {
  display_name  = "online correctness"
  session_id    = data.langsmith_project.default.id
  sampling_rate = 0.1

  evaluator_id = langsmith_evaluator.correctness.id

  webhooks = [
    {
      url_env      = "LANGSMITH_WEBHOOK_URL"
      headers_json = jsonencode({ Authorization = "Bearer redacted" })
    },
  ]

  spend_limit = {
    limit_usd = 25
    window    = "weekly"
  }
}
