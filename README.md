<img src="docs/notcrawl_banner.jpg" alt="notcrawl banner"/>

# 🗞️ notcrawl

`notcrawl` mirrors Notion workspace data into local SQLite and normalized
Markdown so you can search, query, diff, and share your Notion memory without
depending on the Notion UI.

It has three ingestion paths:

- `desktop`: read-only snapshots of the local Notion desktop cache
- `api`: official Notion API sync with rate-limit aware crawling
- `notion-mcp`: targeted repair through a preconfigured Codex Notion connector

SQLite is the canonical archive. Markdown is the durable human/agent surface.
Git share mode publishes normalized snapshots that other machines can subscribe
to without holding Notion credentials.

## Current Scope

- local SQLite storage with FTS5
- read-only local desktop cache ingestion from macOS Notion
- official API page/block/user/comment ingestion
- Notion database metadata and row ingestion through the official API
- current Notion data-source API support plus legacy database endpoint support
- normalized Markdown export organized by Unicode-safe workspace, teamspace, and page paths
- CSV/TSV export for crawled Notion database rows
- compressed JSONL git-share snapshots plus import/update workflows
- terminal archive browser for quick local page/database inspection
- archive status, activity reporting, and SQLite maintenance commands
- read-only SQL access for ad hoc inspection

## Install

```bash
brew install openclaw/tap/notcrawl
```

You can also download archives, `.deb`, or `.rpm` packages from the
[latest release](https://github.com/openclaw/notcrawl/releases/latest).

Check for newer releases manually with:

```bash
notcrawl check-update
```

Interactive terminal runs also perform a cached daily release check and print a
stderr notice when a newer OpenClaw release is available. Set
`NOTCRAWL_NO_UPDATE_CHECK=1` or `CRAWLKIT_NO_UPDATE_CHECK=1` to disable that
passive notice.

## Quick Start

Use the local Notion Desktop cache:

```bash
notcrawl init
notcrawl doctor
notcrawl status
notcrawl report
notcrawl sync --source desktop
notcrawl export-md
notcrawl search "launch plan"
notcrawl tui
```

Or use the official Notion API:

```bash
export NOTION_TOKEN="secret_..."
notcrawl sync --source api
notcrawl databases
notcrawl export-db --database DATABASE_ID --format csv --output roadmap.csv
notcrawl export-db --all --dir exports/csv
```

Or repair incomplete Desktop pages through the Notion app connected in Codex:

```bash
notcrawl sync --source notion-mcp
notcrawl sync --source notion-mcp --page PAGE_ID
notcrawl sync --source notion-mcp --query "launch plan" --limit 25
```

Without `--page` or `--query`, this source fetches known Desktop pages that
have no cached body or missing referenced blocks, plus API pages whose block
sync did not complete. It does not claim to enumerate the entire workspace.
The transport reuses Codex authentication and the configured Notion app through
the experimental ChatGPT apps gateway. Empty connector bodies leave existing
archive content unchanged and remain eligible for a later retry.

Default paths:

- config: `~/.notcrawl/config.toml`
- database: `~/.notcrawl/notcrawl.db`
- cache: `~/.notcrawl/cache`
- Markdown archive: `~/.notcrawl/pages`
- git share repo: `~/.notcrawl/share`

## Commands

- `init` writes a starter config
- `doctor` checks config, SQLite, desktop cache, and token presence
- `status` prints archive counts, last sync time, and database/WAL size
- `metadata --json`, `status --json`, and `doctor --json` expose crawlkit
  control/status payloads for launchers, automation, and CI
- `report` summarizes recent page, database, space, and comment activity
- `maintain` rebuilds FTS, optimizes SQLite indexes, and can run `VACUUM`
- `sync` ingests from `desktop`, `api`, `notion-mcp`, or `all`
- `export-md` renders normalized Markdown files from SQLite
- `databases` lists crawled Notion databases
- `export-db` exports one crawled Notion database, or all databases with `--all --dir`, to CSV or TSV
- `search` searches page and comment text through FTS5
- `tui` opens the terminal archive browser for pages and databases
- `sql` runs read-only SQL against the archive
- `publish` exports SQLite tables and Markdown into a git share repo; `--tag` names an immutable checkpoint
- `subscribe` clones a share repo and imports the latest snapshot
- `update` pulls and imports a subscribed share repo; `--ref` imports a historical tag, commit, or branch without changing the checkout

## Shared crawlkit surfaces

`notcrawl` uses `crawlkit` for standard config paths, SQLite open/read helpers,
snapshot packing/import, git-backed archive sharing, output formatting, status
payloads, and the shared terminal explorer. Notion API/Desktop parsing,
Markdown rendering, page/comment/database schemas, and Notion FTS bodies remain
owned by `notcrawl`.

The TUI follows the gitcrawl-style three-pane model: workspace/teamspace/page or
database groups on the left, pages/databases in the middle, and a readable
document preview plus comments and metadata on the right. It supports pane
focus, sortable headers, mouse selection, right-click actions, and a
local/remote footer.

## Distribution

Release packaging is managed with GoReleaser. Tagged releases build tarballs,
checksums, `.deb`, `.rpm`, GitHub release notes, and a Homebrew tap update.

See [`docs/distribution.md`](docs/distribution.md) for release operations.

## Safety Model

Desktop mode is read-only. It snapshots Notion's local SQLite database before
reading it and never writes to Notion application storage. Desktop cache
coverage is opportunistic; Markdown exports mark pages whose referenced blocks
were not cached locally, and API sync can fill content shared with an integration.
Rows missing from a later Desktop snapshot are preserved because cache eviction
is not a deletion signal; explicit Notion tombstones still retire records.

API mode uses the official Notion API. It stores raw API payloads alongside
normalized rows so renderers can improve without recrawling.

Notion MCP mode is read-only and targeted. It reads the Codex bearer credential
at request time, never stores it, dynamically resolves only the connected
Notion search/fetch tools, and strips signed URL credentials before persisting
connector Markdown, including page properties. Credentials are sent only to the
exact HTTPS ChatGPT apps gateway. The gateway and Codex auth-file format are
experimental contracts and may change.

Secrets are never exported into Markdown or git-share snapshots.
