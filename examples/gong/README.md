# Register Gong as a managed MCP server

This example creates a manual OAuth connection and a workspace MCP server in LangSmith. It does not create the Gong-side integration or authorize a user's Gong account.

## Prerequisites

- Build this provider revision and configure the development override described in the provider README, or use a published release that includes both resources
- Enable Managed Tools for the target LangSmith organization
- Use a user-owned LangSmith API key with permission to create and delete MCP servers and manage workspace integrations; service-account keys cannot manage this catalog
- Set `LANGSMITH_API_KEY`, `LANGSMITH_ENDPOINT`, and `LANGSMITH_WORKSPACE_ID` for the intended environment
- Have a Gong tech admin register a manual MCP integration, as described in [Gong's setup guide](https://help.gong.io/docs/create-an-integration-to-connect-to-the-mcp-server)

Get the LangSmith OAuth callback from the selected environment before registering the Gong integration:

```bash
curl --fail --silent --show-error \
  -H "X-API-Key: $LANGSMITH_API_KEY" \
  -H "X-Tenant-Id: $LANGSMITH_WORKSPACE_ID" \
  "${LANGSMITH_ENDPOINT%/}/v1/agent-auth/oauth/client-metadata.json"
```

Use an origin-only endpoint such as `https://api.smith.langchain.com` for this command. Register the returned `redirect_uris` in Gong. Choose the Gong integration's personal or shared authorization context deliberately: shared access can expose organization-wide data. This example requests only `mcp:read`.

## Build an unreleased provider

For dogfooding before a provider release, run this from the provider source directory:

```bash
mkdir -p /tmp/langsmith-provider-dev
go build -o /tmp/langsmith-provider-dev/terraform-provider-langsmith .
```

Create a Terraform CLI configuration outside the repository and set `TF_CLI_CONFIG_FILE` to its path:

```hcl
provider_installation {
  dev_overrides {
    "langchain-ai/langsmith" = "/tmp/langsmith-provider-dev"
  }
  direct {}
}
```

With this development override, skip `terraform init` and run `terraform plan` and `terraform apply` directly. `init` resolves published versions rather than this unreleased build. Without an override, use a published provider release that includes these resources and run `init` first.

## Apply

Supply the issued client secret to the Terraform process through `GONG_CLIENT_SECRET`, using your secret manager. Do not place it in a Terraform variable or commit it. Set `TF_VAR_gong_client_id` and `TF_VAR_gong_token_endpoint_auth_method` to the values for your registered integration. Confirm the token endpoint authentication method with Gong; the example intentionally has no guessed default.

```bash
terraform plan
terraform apply
```

The OAuth endpoints follow [Microsoft's Gong connector reference](https://learn.microsoft.com/en-us/connectors/gong/); the MCP URL and scopes follow Gong's setup guide. This example has not been verified against a live Gong account.

After applying, connect your Gong account through LangSmith's Managed Tools OAuth flow. Terraform registers the connection and server; it does not complete consent, obtain a user token, or test tool invocation. Use the `gong_mcp_url` output as the managed endpoint.

## Lifecycle

Server configuration changes require replacement because the workspace server API does not support editing those fields or disabling workspace servers. OAuth provider changes, such as scopes or endpoints, update the connection's OAuth app in place, and users keep their authorizations. A new client ID also updates in place, but users must authorize again because their refresh tokens belong to the old client. Changing the value of `GONG_CLIENT_SECRET` alone does not trigger a plan: increment `client_secret_version` on the OAuth connection when rotating it, and the new secret is sent in place. Changing the connection's `slug` replaces the connection and the server, and users must authorize again; do not use `create_before_destroy` with an unchanged connection slug.

Deleting the resources removes the LangSmith server, connection, and its owned provider. It does not delete the integration registered in Gong.
