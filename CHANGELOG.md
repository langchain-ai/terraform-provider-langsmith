# Changelog

## [0.0.20](https://github.com/langchain-ai/terraform-provider-langsmith/compare/v0.0.19...v0.0.20) (2026-11-13)


### Features

* add team subjects to LLM gateway policies ([7b36072](https://github.com/langchain-ai/terraform-provider-langsmith/commit/7b36072985b67699c954b3ba02999c20ff422b52))
* manage LangSmith Managed Tools servers, profiles and kits with Terraform; set up Robin's bot kit ([5cb1162](https://github.com/langchain-ai/terraform-provider-langsmith/commit/5cb1162849c949a844e844e4bedcc292eafdf5d1))
* **terraform:** add tracing key lifecycle resource ([3e053e2](https://github.com/langchain-ai/terraform-provider-langsmith/commit/3e053e2bc2a22d53eea45ac1d9c9756db656742d))
* **terraform:** target deployments to agent environments ([2e42df1](https://github.com/langchain-ai/terraform-provider-langsmith/commit/2e42df150fa5526a6546985a2806ad8b17b834db))

## [0.0.19](https://github.com/langchain-ai/terraform-provider-langsmith/compare/v0.0.18...v0.0.19) (2026-11-08)


### Features

* count Terraform provider requests as a terraform client ([bdf1796](https://github.com/langchain-ai/terraform-provider-langsmith/commit/bdf1796739e0f9f9761109e8c40f3aa636e3787d))
* **terraform:** manage registry agents and expose environments ([6420a9f](https://github.com/langchain-ai/terraform-provider-langsmith/commit/6420a9f66ecdd3999512de55f980e914c3403f57))
* **terraform:** support data_plane_id on langsmith_workspace ([71932ae](https://github.com/langchain-ai/terraform-provider-langsmith/commit/71932ae235d7aa11485d2f6718140e532480a28a))

## [0.0.18](https://github.com/langchain-ai/terraform-provider-langsmith/compare/v0.0.17...v0.0.18) (2026-10-30)


### Bug Fixes

* **terraform:** preserve role permission order when grants are unchanged ([1cb78d9](https://github.com/langchain-ai/terraform-provider-langsmith/commit/1cb78d9b99fa3f1445454ec7f6b088a4d6224c04))
* **terraform:** recover memberships after workspace deletion ([8c1683e](https://github.com/langchain-ai/terraform-provider-langsmith/commit/8c1683e521fa03baad78acf18d859b6f0987ef35))

## [0.0.17](https://github.com/langchain-ai/terraform-provider-langsmith/compare/v0.0.16...v0.0.17) (2026-10-26)


### Bug Fixes

* allow empty workspace role descriptions ([5ec8daf](https://github.com/langchain-ai/terraform-provider-langsmith/commit/5ec8dafd0606c0d9b97dafe6fa3564107f71096a))
* allow empty workspace role descriptions ([099f3c8](https://github.com/langchain-ai/terraform-provider-langsmith/commit/099f3c85e5a2348f5bd2394f551c74b0cd4dddc8))
* **terraform:** create workspace memberships for pending org invites ([ec46f23](https://github.com/langchain-ai/terraform-provider-langsmith/commit/ec46f2365ef576959672c123d652ba545844f3f8))

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
