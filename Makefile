.PHONY: build test docs

build:
	go build -o bin/terraform-provider-langsmith .

test:
	go test ./...

# Generate Terraform Registry documentation from the provider schema and
# examples/. Run before tagging a release — the registry rejects providers
# without a docs/ tree.
docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest generate --provider-name langsmith
