output "langsmith_version" {
  description = "LangSmith server version returned by the local profile probe."
  value       = data.langsmith_info.current.version
}
