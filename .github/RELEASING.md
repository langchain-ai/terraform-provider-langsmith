# Release process

This document is the source of truth for releasing
`langchain-ai/terraform-provider-langsmith`.

Releases are cut from this public repository because the Terraform Registry
requires it. The copy at `langchainplus/sdks/terraform` is a Copybara mirror;
tagging the monorepo does not release the provider.

## Versioning

There is no version file to bump. `main.go` contains `var version = "dev"`, and
GoReleaser replaces it at build time with the version from the Git tag. Pushing
the tag is the version bump.

Use the next appropriate semantic version. To see existing releases:

```bash
git tag --list 'v*' --sort=-v:refname | head
```

## Before tagging

1. Land the release contents on `main` through a pull request. Copybara syncs
   also land through pull requests.
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

## Tag and publish

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
