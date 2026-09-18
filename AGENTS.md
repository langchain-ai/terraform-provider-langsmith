# Workspace-scoped resources

- Workspace-scoped resources must support an optional per-resource `workspace_id` override, falling back to the provider's workspace when omitted, so one provider configuration can manage multiple workspaces.
- Apply the selected workspace consistently to create, read, update, delete, import, and cleanup requests; preserve it in Terraform state without mutating the shared provider client.
- Require replacement when changing `workspace_id` unless the API explicitly supports moving the resource between workspaces.
- Cover both explicit workspace overrides and provider-default fallback in focused tests, and update resource examples and generated docs.
- Organization-scoped resources do not need a workspace selector; resources that already require a workspace ID or derive ownership from their own identity should retain that routing.
