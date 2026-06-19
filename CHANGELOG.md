# Changelog

## 0.5.1 - Unreleased

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
