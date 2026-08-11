# Changelog

## v0.0.6 (2026-08-10)

v0.0.6 adds Terraform support for LangSmith resource tags and attribute-based access control (ABAC). You can now define workspace-scoped tags, apply them to LangSmith resources, create organization-scoped access policies based on those tags, and attach policies to workspace roles.

### New resources

- [`langsmith_tag_key`](docs/resources/tag_key.md) manages a reusable tag key in the provider's configured workspace.
- [`langsmith_tag_value`](docs/resources/tag_value.md) manages values for a tag key.
- [`langsmith_tagging`](docs/resources/tagging.md) applies a tag value to a supported LangSmith resource. Tag assignments have an independent lifecycle, so they can be changed without recreating their key or value.
- [`langsmith_tag`](docs/resources/tag.md) provides a convenient combined lifecycle for a dedicated tag key and value.
- [`langsmith_access_policy`](docs/resources/access_policy.md) manages an organization-scoped ABAC policy. Policies support `allow` and `deny` effects, multiple condition groups, and tag comparisons including exact, case-insensitive, pattern-matching, and “if exists” operators.
- [`langsmith_access_policy_attachment`](docs/resources/access_policy_attachment.md) attaches an access policy to a workspace role. Attachments are managed separately so a policy can be shared across roles and role assignments can change without recreating the policy.

All six resources support import, making it possible to bring existing tags, tag assignments, policies, and policy attachments under Terraform management.

### Reliability and testing

- Added lifecycle recovery for the new resources when remote objects are removed outside Terraform.
- Added an offline end-to-end Terraform acceptance suite covering create, update, no-op plans, replacement, removal, and destroy behavior for the new resources.

There are no breaking configuration changes for existing provider resources in this release.

[Full comparison](https://github.com/langchain-ai/terraform-provider-langsmith/compare/v0.0.5...v0.0.6)
