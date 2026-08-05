# Terraform Provider for LangSmith

This is the first-party LangSmith Terraform provider scaffold.

The provider currently includes:

- Provider configuration that delegates auth, endpoint, workspace, and profile loading to the official LangSmith Go SDK.
- `data.langsmith_info`, which probes `/api/v1/info`.
- `data.langsmith_project`, which resolves a tracing project/session by exact name.
- `data.langsmith_org`, which reads the current organization.
- `data.langsmith_workspace`, which looks up workspaces by `id` or `display_name`.
- `data.langsmith_org_role`, which looks up organization roles by `name` or `display_name`.
- `data.langsmith_workspace_role`, which looks up workspace roles by `name` or `display_name`.
- `data.langsmith_permissions`, which lists role permission names by access scope.
- `langsmith_workspace`, which manages LangSmith workspaces.
- `langsmith_workspace_role`, which manages custom workspace-scoped roles.
- `langsmith_alert_rule`, with create/read/update/delete/import support for `/v1/platform/alerts/{session_id}`.
- `langsmith_org_membership`, which manages desired org membership by email and org role.
- `langsmith_workspace_membership`, which manages desired workspace membership by workspace, email, and workspace role.
- `langsmith_tag_key`, `langsmith_tag_value`, and `langsmith_tagging`, which manage the independent resource-tag lifecycles.
- `langsmith_tag`, a convenience resource that owns one tag key and one value.
- `langsmith_access_policy`, which manages ABAC access policies.
- `langsmith_access_policy_attachment`, which attaches an access policy to a workspace role.

## Resource Tags and ABAC

Use the independent resources when tag keys or values are shared. `langsmith_tag` is a convenience lifecycle for a dedicated key and value; taggings remain separate so changing the tagged resource does not recreate the key or value.

Access policies are organization-scoped, while tag keys, values, and taggings are scoped to the workspace selected by the provider. Manage each workspace-role association independently with `langsmith_access_policy_attachment`.

## Access Management Resources

Organization creation is not supported by the LangSmith API, so the provider reads the current organization from the configured SDK context.

Membership resources are keyed by normalized email and expose `status` as computed state:

- `pending` means LangSmith has an outstanding invite.
- `active` means the invite has been accepted and LangSmith returned an active membership record.

This keeps resource IDs stable when a user accepts an invite. `langsmith_workspace_membership` creates active workspace membership only after the email has an accepted organization membership; it does not create pending workspace invites because the batch invite API also chooses organization role behavior.

Example:

```hcl
data "langsmith_org_role" "org_user" {
  name = "ORGANIZATION_USER"
}

data "langsmith_workspace_role" "workspace_user" {
  name = "WORKSPACE_USER"
}

resource "langsmith_workspace" "dev" {
  display_name  = "Access Management Dev"
  tenant_handle = "access-management-dev"
}

resource "langsmith_org_membership" "alice" {
  email   = "alice@langchain.dev"
  role_id = data.langsmith_org_role.org_user.id
}

resource "langsmith_workspace_membership" "alice_dev" {
  workspace_id = langsmith_workspace.dev.id
  email        = "alice@langchain.dev"
  role_id      = data.langsmith_workspace_role.workspace_user.id
}
```

## Access Management Data Sources

Built-in system roles are read-only and should be referenced with `data.langsmith_org_role` or `data.langsmith_workspace_role` rather than managed as custom roles. Custom roles are currently workspace-scoped and managed with `langsmith_workspace_role`.

```hcl
data "langsmith_org" "current" {}

data "langsmith_workspace" "dev" {
  display_name = "Access Management Dev"
}

data "langsmith_org_role" "org_user" {
  name = "ORGANIZATION_USER"
}

data "langsmith_workspace_role" "workspace_user" {
  name = "WORKSPACE_USER"
}

data "langsmith_permissions" "available" {}

resource "langsmith_workspace_role" "project_operator" {
  display_name = "Project Operator"
  description  = "Can read and create projects"
  permissions  = [
    "projects:read",
    "projects:create",
  ]
}
```

Existing memberships can be imported by email:

```shell
terraform import langsmith_org_membership.alice alice@langchain.dev
terraform import langsmith_workspace_membership.alice_dev <workspace_id>/alice@langchain.dev
```

The synthetic IDs are also accepted:

```shell
terraform import langsmith_org_membership.alice org/current/email/alice@langchain.dev
terraform import langsmith_workspace_membership.alice_dev workspace/<workspace_id>/email/alice@langchain.dev
```

## Local development

Build the provider:

```shell
make build
```

Use a Terraform CLI development override:

```hcl
provider_installation {
  dev_overrides {
    "langchain-ai/langsmith" = "/absolute/path/to/terraform-provider-langsmith/bin"
  }
  direct {}
}
```

Then point Terraform at the override file with `TF_CLI_CONFIG_FILE`.

For local LangSmith testing, the SDK will use `LANGSMITH_PROFILE`, the current profile in `~/.langsmith/config.json`, or an explicitly configured profile:

```hcl
provider "langsmith" {
  profile = "local"
}
```

For non-local usage, prefer environment variables:

- `LANGSMITH_API_KEY`
- `LANGSMITH_ENDPOINT`
- `LANGSMITH_WORKSPACE_ID` or `LANGSMITH_TENANT_ID`

With those variables set, the provider can be empty:

```hcl
provider "langsmith" {}
```

Webhook actions can include non-sensitive URLs directly in `config_json`. Use `url_env` when a webhook URL should be kept out of Terraform state; the provider reads that environment variable during create/update, removes `url` from state after reads, and detects rotations of the referenced value during planning.

Run the opt-in local CRUD smoke test with a LangSmith CLI profile:

```shell
LANGSMITH_PROVIDER_ACC=1 LANGSMITH_PROFILE=local TEST_LANGSMITH_WEBHOOK_URL=https://example.com/webhook go test ./internal/provider -run TestAccAlertRuleCRUDLocal -count=1 -v
```

The smoke test creates, updates, deletes, and then cleans up a temporary local tracing project/session and alert rule.

Run the ABAC and resource-tag acceptance lifecycle entirely offline with Terraform 1.11.2:

```shell
TF_ACC=1 go test ./internal/provider -run '^TestAccABACResourcesOffline$' -count=1 -v
```

This test uses an in-process contract server and exercises Terraform create, update, optional-description clearing, access-policy attachment replacement/removal, no-op planning, and destroy for `langsmith_tag_key`, `langsmith_tag_value`, `langsmith_tagging`, `langsmith_tag`, `langsmith_access_policy`, and `langsmith_access_policy_attachment`. It does not use LangSmith credentials or contact a deployed environment.
