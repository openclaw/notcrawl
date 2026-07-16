# Distribution

`notcrawl` ships through GitHub Releases, Homebrew tap updates, and optional
Cloudsmith APT/RPM repositories.

Official macOS archives are signed with identifier `org.openclaw.notcrawl` by
the OpenClaw Foundation Apple Developer Team `FWJYW4S8P8`. Ordinary builds and
GoReleaser snapshots do not need signing credentials. The official publish path
is local to an authorized maintainer Mac and fails closed unless both signed
architecture archives pass verification.

## Local Checks

```bash
go test ./...
go build ./cmd/notcrawl
```

Also smoke the crawlkit control and non-interactive TUI surfaces before a tag:

```bash
notcrawl metadata --json
notcrawl status --json
notcrawl doctor --json
notcrawl check-update --json
notcrawl tui --json --limit 10
```

The CI workflow runs the same control-surface smoke checks, plus dependency
verification, `gofmt`, `go vet`, tests, a GoReleaser snapshot build, and
CodeQL. Snapshot macOS binaries are development artifacts and are not eligible
for publication.

If GoReleaser is installed:

```bash
make release-snapshot
```

That creates local snapshot archives, checksums, `.deb`, and `.rpm` packages
under `dist/` without publishing.

## Release Notes

GitHub uses Release Drafter to auto-label PRs and maintain draft release notes
from merged pull requests. The maintainer verifies those notes against the
changelog before locally publishing the prepared assets.

## Tagged Release

Prepare the changelog and create a signed semver tag. From a clean Apple Silicon
checkout at that exact tag, build all official assets through the managed local
signing path:

```bash
git tag -s v0.1.0 -m "notcrawl v0.1.0"
make release-artifacts TAG=v0.1.0
```

This builds Linux archives and packages without credentials, then uses
`release-mac-app codesign-run` to build the two signed macOS archives. The step
requires Rosetta so both architectures can execute. It rejects a dirty or
mismatched checkout, an invalid tag signature, a missing managed identity, the
wrong Team ID or identifier, stale build provenance, incomplete archive
contents, and checksum/asset-set mismatches. Private keychain and credential
routing belongs in the ignored `.mac-release.env` or another approved runtime
environment.

After inspecting the exact 11-file manifest in `dist/release-assets/`, push
`main` and the tag, attach those files to the Release Drafter draft, verify its
notes contain the changelog, then publish it locally. The local preparation
step is the pre-publication gate: it verifies the exact manifest, checksums,
binary provenance, and both Developer ID signatures before any upload. CI never
imports the Developer ID private key and never publishes release assets.

The read-only `Release Validation` workflow runs after publication using
verifier code from the trusted default branch. It checks the exact asset
manifest and checksums, tests the tagged source, verifies every Linux
archive/package binary embeds the tag commit, and verifies each macOS binary
natively against the Apple trust chain, Team ID, identifier, version,
architecture, and embedded tag commit. The Homebrew workflow then updates the
tap; Cloudsmith publication remains an explicit follow-up.

## Required Secrets

- `HOMEBREW_TAP_GITHUB_TOKEN`: token that can push to the tap repository
- `CLOUDSMITH_API_KEY`: optional; enables package publishing

The Apple Developer private key is never a GitHub Actions secret. It is used
only by the managed local signing helper during official release preparation.

## Optional Variables

- `HOMEBREW_TAP_REPO`: defaults to `openclaw/tap`
- `CLOUDSMITH_APT_TARGETS`: comma-separated targets like `ubuntu/jammy,debian/trixie`
- `CLOUDSMITH_DISTRIBUTION` and `CLOUDSMITH_RELEASE`: legacy single APT target
- `CLOUDSMITH_RPM_DISTRIBUTION`: defaults to `el`
- `CLOUDSMITH_RPM_RELEASE`: defaults to `9`

## Manual Reruns

If Cloudsmith publish fails after GitHub release assets exist:

```bash
gh workflow run publish-apt.yml -f tag_name=v0.1.0
gh workflow run publish-rpm.yml -f tag_name=v0.1.0
```

If the Homebrew tap update fails:

```bash
gh workflow run homebrew-tap.yml -f tag_name=v0.1.0
```
