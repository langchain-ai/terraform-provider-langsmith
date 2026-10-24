# Release process

This document is the source of truth for releasing
`langchain-ai/terraform-provider-langsmith`.

Releases are cut from this public repository because the Terraform Registry
requires it. Copybara exports the development source to this public repository;
tagging the development monorepo does not release the provider.

## Versioning

Release Please maintains a release pull request on each push to public `main`,
using Conventional Commits (`fix:`, `feat:`, and `feat!:` or `BREAKING CHANGE:`).
Copybara preserves the source commit title, or its `SDK-Note:` entries when
present, for release detection.

The release PR updates `CHANGELOG.md` and `.release-please-manifest.json`.
These files stay public-owned and are excluded from export. Workflow files,
`release-please-config.json`, and this runbook are maintained in the development
source and exported by Copybara; do not edit them only in the public repository.
The nested workflows execute only after export, not in the development source.

Before 1.0, fixes and features bump the patch version; breaking changes bump
the minor version. Tags use `vX.Y.Z` without a component prefix. `main.go`
stays at `var version = "dev"`; GoReleaser injects the tag version at build time.

## Before releasing

Development-source CI runs vet, unit tests, lint, build, offline Terraform
acceptance tests, and docs drift checks before export. Public CI additionally
checks vet, unit tests, and builds on Linux, macOS, and Windows. Neither runs
credentialed live acceptance tests by default.

1. Land the release contents through the development source review process,
   then wait for Copybara to export them to this public repository’s `main`.
   Do not merge public-only source changes: the next export would overwrite them.
2. Synchronize local `main` and record the exact commit to release:

   ```bash
   git switch main
   git pull --ff-only
   main_sha=$(git rev-parse HEAD)
   ```

3. Confirm the push-triggered CI run for that exact SHA completed successfully:

   ```bash
   gh run list \
     --workflow CI \
     --branch main \
     --event push \
     --commit "$main_sha" \
     --json databaseId,status,conclusion,url

   gh run view RUN_ID
   ```

   Treat all five jobs in `.github/workflows/ci.yml` as release requirements,
   regardless of which checks the branch rules currently require:

   - `test (ubuntu-latest)`: vet, unit tests, Terraform 1.11.2 setup,
     `make testacc`, and build
   - `test (macos-latest)`: vet, unit tests, and build
   - `test (windows-latest)`: vet, unit tests, and build
   - `lint`: golangci-lint v2.12.2
   - `generate`: `make generate` using tfplugindocs v0.25.0, followed by a
     Git diff check

   If `make generate` changes `docs/`, commit the generated files before
   merging. Running plain `make` is not equivalent to CI.

## Stable releases

Enable **Allow GitHub Actions to create and approve pull requests** in the
public repository's Actions settings. Release Please uses `GITHUB_TOKEN` with
`contents: write` and `pull-requests: write`; only the publishing job imports
the existing GPG signing secrets.

PRs created or updated with `GITHUB_TOKEN` do not automatically trigger other
Actions workflows. A maintainer must push a commit to the release PR branch
(an empty commit is sufficient) to trigger normal PR CI. Repeat after bot
updates, and do not bypass CI requirements.

Review the version and changelog, confirm public CI passed for the latest
exported changes and the release PR, then merge the release PR. Release Please
creates the tag and GitHub Release. The same workflow runs GoReleaser at the
released SHA to upload signed Registry artifacts, since `GITHUB_TOKEN`-created
tags do not trigger another workflow. Verify artifacts and Registry ingestion
as described below.

## Development candidates

Stable releases use the release PR rather than manually pushed tags.
For a development candidate, use a SemVer prerelease tag such as
`v0.0.14-rc.1` on the tested feature-branch commit. Confirm the five CI jobs
listed above passed for that commit before tagging. GoReleaser marks these
releases as prereleases; stable releases continue to come from `main`.
Consumers must pin the exact prerelease version in `required_providers`.

Set `version` to the next SemVer tag, then create and push an annotated tag:

```bash
version=vX.Y.Z
git tag -a "$version" -m "$version"
git push origin "$version"
```

The tag starts `.github/workflows/release.yml`. The workflow imports the GPG
key, runs `goreleaser release --clean`, and creates a non-draft GitHub Release.
It normally takes about six minutes.

Find and watch the run for the released commit:

```bash
gh run list \
  --workflow release.yml \
  --event push \
  --commit "$main_sha" \
  --json databaseId,status,conclusion,url

gh run watch RUN_ID --exit-status
```

Confirm the GitHub Release contains the platform archives, version manifest,
checksums, and detached signature:

```bash
gh release view "$version" \
  --json tagName,publishedAt,isDraft,isPrerelease,url,assets
```

## Verify Terraform Registry ingestion

The Terraform Registry normally ingests the GitHub Release within seconds.
Verify that its API lists the exact version, protocol, and expected platforms:

```bash
registry_version=$(printf '%s' "$version" | sed 's/^v//')
curl --silent --show-error \
  https://registry.terraform.io/v1/providers/langchain-ai/langsmith/versions \
  | jq --arg version "$registry_version" \
      '.versions[] | select(.version == $version)'
```

The release is also visible at
<https://registry.terraform.io/providers/langchain-ai/langsmith/latest>.

If a prerelease is missing from the Registry, its webhook may have ignored the
GitHub prerelease event. Temporarily clear the GitHub flag while keeping the
release out of the latest slot:

```bash
gh release edit "$version" --prerelease=false --latest=false
```

Wait until the Registry versions API lists the exact RC, then restore the flag:

```bash
gh release edit "$version" --prerelease --latest=false
```

Keep the SemVer prerelease tag unchanged throughout. Verify both Registry
availability and the stable latest GitHub release afterward.

## Release artifacts

`.goreleaser.yml` defines the artifact shape expected by the Registry. Do not
change it casually. A release contains:

- Zip archives for Linux, macOS, Windows, and FreeBSD across the configured
  architectures, with binaries named
  `terraform-provider-langsmith_v<version>`
- `terraform-provider-langsmith_<version>_manifest.json`
- `terraform-provider-langsmith_<version>_SHA256SUMS`, including the Registry
  manifest checksum
- A detached GPG signature for the checksums file
- No generated changelog (`changelog.disable: true`)

The release workflow reads `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` from GitHub
Actions secrets and derives `GPG_FINGERPRINT` during key import. Never place
secret values in this repository or this runbook.

## When to use the Terraform Registry UI

There is no per-release publish button. Use the Registry UI only for:

- One-time provider onboarding and publishing the organization signing key
- Troubleshooting a GitHub Release that was not ingested; inspect repository
  webhook deliveries, then re-sync from the provider's Registry page
- Signing-key rotation; upload the new public key before the first release
  signed with it
