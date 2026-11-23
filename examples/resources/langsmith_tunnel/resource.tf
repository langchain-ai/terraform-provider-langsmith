# A tunnel lets LangSmith reach named services on your network. Run the
# connector with `langsmith tunnel connect <id> --target app=http://localhost:PORT`.
resource "langsmith_tunnel" "vpc" {
  name        = "vpc"
  description = "Services in the private VPC"
}
