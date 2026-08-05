# Changelog

## 0.5.7 - 2026-08-02

### Fixed

- Bound default Notion API requests to 60 seconds so a stalled peer cannot hang sync indefinitely. Thanks @SebTardif.

### Dependencies

- Update CrawlKit to v0.14.5, modernc.org/sqlite to v1.56.0, indirect Go modules, and the vulnerability and dead-code tooling.

## 0.5.6 - 2026-08-02

### Dependencies

- Update CrawlKit to v0.14.4, modernc.org/sqlite to v1.55.0, and refresh the TruffleHog GitHub Action with an immutable version pin.

### Tooling

- Let the unified release workflow create the immutable release tag and consume organization-managed signing, notarization, and Homebrew credentials without local secret access.
- Accept the shared release pipeline's normalized archive member paths in local release verification.

## 0.5.5 - 2026-07-26

### Dependencies

- Update terminal and text support dependencies, including `go-isatty` v0.0.24, `go-runewidth` v0.0.27, and `x/text` v0.40.0.

### Tooling

- Standardize the maintainer Make targets and keep the official local release path fail-closed on signing and notarization verification.
- Move releases to the shared OpenClaw CI pipeline, including signed and notarized macOS archives, GitHub-hosted Linux packages, and verified Homebrew handoff.

## 0.5.4 - 2026-07-17

### Highlights

- Make git-share imports non-destructive by default, preserving local-only rows and tombstones while reserving exact replacement for explicit `--restore` mode.
- Notarize both official macOS binaries and require Apple notarization verification before release publication.

### Sharing and retention

- Preserve stable row identity and mixed-source fallback payloads during share merges, with optional overwritten-payload history through `--retain-revisions`.
- Record deletion time, source, and reason for explicit deletes, authoritative enumeration, and parent-delete events.

### Dependencies

- Update CrawlKit to v0.14.3.

### Release infrastructure

- Fail closed without an approved runtime notarization profile and verify both submissions again when validating release archives.
- Add credential-free macOS release-script regressions to hosted CI.

## 0.5.3 - 2026-07-17

### Highlights

- Keep Notion API sync running when unsupported block children appear, preserve previously mirrored page content, and leave incomplete pages eligible for repair. Thanks @oliver-mee.
- Retry transient request and response-body transport failures for read-only Notion API operations without replaying mutating requests. Thanks @davelutztx.

### Dependencies

- Update CrawlKit to v0.14.2.
- Update Kong to v1.16.0.
- Update modernc.org/sqlite to v1.54.0.

### Tooling

- Update the GoReleaser, stale-issue, Release Drafter, and TruffleHog GitHub Actions.

## 0.5.2 - 2026-07-09

- Sign official macOS release binaries with the OpenClaw Foundation Developer ID while keeping local and cross-platform builds credential-free.
- Update CrawlKit to v0.13.4 and refresh its hardened TOML dependency.
- Update Go to 1.26.5 to resolve GO-2026-5856 in `crypto/tls`.

## 0.5.1 - 2026-06-19

- Retry concurrent Git snapshot branch-and-tag pushes after rebasing and retargeting the unpublished tag.
- Retry transient Cloudflare 524 timeouts from Notion API requests. Thanks @davelutztx.
- Update `golang.org/x/sys` to v0.46.0, resolving a Windows integer-overflow advisory in the dependency graph.
- Add immutable Git share snapshot tags and non-mutating historical imports with `update --ref`.
- Move Desktop SQLite bundle snapshots, Markdown sidecar synchronization, and Git share commits/remotes onto CrawlKit; refresh Go dependencies.

## 0.5.0 - 2026-06-17

- Add targeted `notion-mcp` sync through a preconfigured Codex Notion app to repair incomplete Desktop/API pages and fetch explicit page IDs or bounded search results.
- Mark incomplete Desktop-cache Markdown pages with missing-block metadata and warnings instead of silently exporting title-only files.
- Render page and database-row properties in Markdown exports with collection schema labels when available.
- Preserve previously cached Desktop pages and blocks across opportunistic cache eviction while honoring explicit tombstones.
- Track API and Desktop provenance independently so archived API records restore live Desktop page, block, and comment payloads.
- Preserve source provenance in git-share snapshots and validate complete manifests before replacing a local archive.
- Update `crawlkit` to v0.12.2.
- API sync now tolerates Notion `restricted_resource` failures from `/users`, continues page/database discovery without user labels, and warns when API discovery returns no pages, databases, blocks, or comments. Thanks @elijahmuraoka.
- API sync now skips fetching copied synced-block children that Notion reports as `object_not_found`, while still storing the synced-block copy. Thanks @elijahmuraoka.
- Support Notion Desktop defaults and stale-directory pruning on Windows. Thanks @MrJngomonkey.

## 0.4.0 - 2026-05-18

- Move top-level CLI parsing plus `search` and `sql` argument parsing onto Kong while preserving existing help, config, and output behavior.
- Support `notcrawl search --help`, `notcrawl sql --help`, and `notcrawl search --limit N` without loading config for help output.
- Add cached release checks with `notcrawl check-update` and passive terminal
  notices when a newer OpenClaw release is available.

- Bump routine GitHub Actions dependencies.

- Add a repo-local `notcrawl` agent skill for local archive, freshness, query,
  and verification workflows.
- Document `notcrawl sql` read-only query examples in the repo-local agent
  skill so agents can do exact archive counts and inventory checks safely.
- Replace the single validation workflow with CI jobs for dependencies,
  formatting/vet, tests, CLI control-surface smoke checks, and GoReleaser
  snapshot builds.
- Add CodeQL analysis on pull requests, `main`, the crawlkit integration branch,
  weekly schedule, and manual dispatch.
- Depend on `github.com/openclaw/crawlkit v0.4.0` for shared config,
  status/control, snapshot, mirror, output, and terminal explorer mechanics.
- Keep Notion API/Desktop parsing, Markdown rendering, page/comment/database
  schemas, Notion FTS body construction, and data-source compatibility
  app-owned while the shared mechanics move to crawlkit.
- Document the gitcrawl-style document TUI shape: workspace/teamspace/page or
  database groups, page/database rows, preview/comment detail, sorting, mouse
  selection, right-click actions, and local/remote status chrome.
- Add crawlkit control metadata/status surfaces with `metadata --json`, `status --json`, and `doctor --json`.
- Report primary archive and desktop-cache SQLite inventories in status JSON for shared local control surfaces.
- Add `notcrawl tui`, a local terminal browser for archived pages and databases backed by `crawlkit/tui`.
- Render TUI rows with compact panes so page and database metadata stays in context/detail instead of crowding the row list.
- Resolve database parent names for the TUI parent pane so collection nesting is readable instead of raw IDs.
- Hide noisy block-derived Notion parent labels in the TUI by falling back to the workspace label when parent text contains raw Notion identifiers.
- Resolve block-parent pages to their owning page when possible so the TUI parent pane shows real Notion hierarchy instead of broad workspace buckets.
- Normalize workspace-level Notion parents as `Workspace: <name>` so the TUI left pane does not split the same workspace into duplicate parent groups.
- Inherit shared crawlkit TUI improvements for newest-first startup, count-header sorting, preview-first document detail panes, and gitcrawl-style metadata labels.
- Feed longer, block-shaped Notion page previews into the TUI detail pane so pages read more like documents instead of flat metadata.
- Include page comments in Notion TUI previews after block content.
- Route the TUI through read-only SQLite access and cover the JSON fallback in tests.
