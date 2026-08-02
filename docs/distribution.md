# Distribution

`notcrawl` ships through GitHub Releases and the `openclaw/homebrew-tap`
formula. The only official publication path is
`.github/workflows/release-unified.yml`, which delegates to the shared OpenClaw
Go CLI release pipeline.

Official macOS archives are signed with hardened runtime and stable identifier
`org.openclaw.notcrawl`, then notarized by Apple. Ordinary local builds and
GoReleaser snapshots remain credential-free.

## Local Checks

Run the complete local gate before preparing a release:

```bash
make check
```

The gate covers dependency verification, formatting, vet, dead-code analysis,
tests, control-surface smoke tests, release configuration validation, and both
GoReleaser snapshot configurations. Snapshot targets remain local diagnostics
and never publish:

```bash
make snapshot
make snapshot-release
```

`make release`, `make release-artifacts`, and `make release-macos` deliberately
refuse local publication and print the official workflow command.

## Official Release

Stamp the changelog section with the release date, merge it to protected
`main`, then dispatch the unified workflow:

```bash
gh workflow run release-unified.yml --repo openclaw/notcrawl -f version=0.5.5
```

The workflow freezes the protected source revision, creates the immutable
annotated tag, verifies independent CI, builds the GoReleaser matrix and nFPM
packages, signs and notarizes both macOS architectures, verifies the complete
draft independently on Intel and Apple Silicon, publishes the GitHub release,
and verifies the Homebrew handoff.

The published `v0.5.4` contract contained six Linux assets: two archives, two
Debian packages, and two RPM packages. The unified workflow preserves all six
names and adds two signed and notarized macOS archives, the aggregate
`sha256sums.txt` manifest, and three release-verification controls. Universal
Darwin packaging is disabled, so the official twelve-asset set is:

```text
notcrawl_VERSION_linux_amd64.tar.gz
notcrawl_VERSION_linux_arm64.tar.gz
notcrawl_VERSION_amd64.deb
notcrawl_VERSION_arm64.deb
notcrawl-VERSION-1.x86_64.rpm
notcrawl-VERSION-1.aarch64.rpm
notcrawl_VERSION_darwin_amd64.tar.gz
notcrawl_VERSION_darwin_arm64.tar.gz
sha256sums.txt
ASSET-INVENTORY.json
RELEASE-NOTES.md
SIGNING-MANIFEST.json
```

Every archive includes `notcrawl`, `CHANGELOG.md`, `LICENSE`, `README.md`,
`SPEC.md`, and `config.example.toml`. The inventory binds each payload to its
size and SHA-256 digest, the signing manifest records the verified macOS
identity and notarization policy, and the frozen release notes are the exact
text published on the release. The Debian and RPM packages remain attached
directly to the GitHub release.

## Diagnostics

Download a published release into `dist/release-assets/`, fetch its tag, then
verify the exact manifest, aggregate checksums, Linux package provenance, and
macOS signatures from a Mac:

```bash
make verify-release TAG=v0.5.5
```

`make verify-release-macos TAG=v0.5.5` verifies already-downloaded macOS
archives in `dist/`. These targets are read-only diagnostics; they never upload
or alter a release.

## Organization Secrets

The workflow receives release credentials from GitHub organization secrets;
release operators never load them locally:

- `MACOS_SIGNING_P12`
- `MACOS_SIGNING_P12_PASSWORD`
- `ASC_KEY_ID`
- `ASC_ISSUER_ID`
- `ASC_PRIVATE_KEY_P8`
- `HOMEBREW_TAP_TOKEN`, mapped to the shared workflow's `TAP_TOKEN`
