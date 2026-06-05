.PHONY: build test

build:
	go build -o bin/terraform-provider-langsmith .

test:
	go test ./...
