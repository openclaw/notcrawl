# notcrawl 🗞️ — Your Notion memory, on disk

[![CI](https://img.shields.io/github/actions/workflow/status/openclaw/notcrawl/ci.yml?branch=main&style=flat-square&label=ci)](https://github.com/openclaw/notcrawl/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/openclaw/notcrawl?style=flat-square)](https://github.com/openclaw/notcrawl/releases/latest)
[![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux-blue?style=flat-square)](https://github.com/openclaw/notcrawl/releases/latest)
[![License](https://img.shields.io/github/license/openclaw/notcrawl?style=flat-square)](LICENSE)
[![Homebrew](https://img.shields.io/badge/Homebrew-openclaw%2Ftap-orange?style=flat-square&logo=homebrew)](https://github.com/openclaw/homebrew-tap/blob/main/Formula/notcrawl.rb)

<img src="docs/notcrawl_banner.jpg" alt="notcrawl banner"/>

`notcrawl` mirrors a Notion workspace into local SQLite and normalized Markdown. It is for people and agents that need to search, query, diff, or share workspace history without depending on the Notion UI.

SQLite is the canonical archive. Markdown is the durable human-readable export.

## Install

Homebrew is the smallest install path on macOS and Linux:

```sh
brew install openclaw/tap/notcrawl
```

GitHub Releases also provide signed and notarized macOS archives, Linux archives, and `.deb` and `.rpm` packages. Download the appropriate file from the [latest release](https://github.com/openclaw/notcrawl/releases/latest).

## Quick start

With Notion Desktop installed and opened at least once:

```sh
notcrawl sync --source desktop
notcrawl search "launch plan"
notcrawl export-md
```

The sync reads a snapshot of Notion's local cache. Search uses the SQLite FTS5 index, and `export-md` writes the normalized archive under `~/.notcrawl/pages`.

Run `notcrawl doctor` if the desktop cache is not found.

## Choose a source

| Source | Use it for | Setup |
|---|---|---|
| `desktop` | Fast, local ingestion of pages Notion Desktop has cached | None beyond Notion Desktop |
| `api` | Pages, blocks, users, comments, databases, and rows shared with an integration | Set `NOTION_TOKEN` |
| `notion-mcp` | Targeted repair of incomplete pages through the Notion app connected in Codex | Configure the experimental connector in `config.toml` |

The official API is the stable remote integration:

```sh
export NOTION_TOKEN="secret_..."
notcrawl sync --source api
```

For a slow or stalled sync, add `--verbose` to that invocation:

```sh
notcrawl sync --source api --verbose
```

Verbose diagnostics go to stderr and report source phases, elapsed time, and counts. Official API sync also reports request attempts, endpoint classes, numeric HTTP statuses, and retry delays. No credentials, headers, request/response bodies, raw URLs, cursors, page identifiers, or upstream error text are logged. Warnings become counts and failures use a fixed message in verbose mode. Normal stdout is unchanged and may still contain local paths; without `--verbose`, the existing progress, warnings, and errors are unchanged. Desktop and MCP sync report source phases and counts, not per-request API traces.

Notion MCP can repair known incomplete pages, fetch a page by ID or URL, or run a bounded workspace search:

```sh
notcrawl sync --source notion-mcp --page PAGE_ID
notcrawl sync --source notion-mcp --query "launch plan" --limit 25
```

Without `--page` or `--query`, the MCP source retries known Desktop pages with missing cached content and API pages with incomplete block sync. It does not enumerate the entire workspace. Empty connector responses leave archived content unchanged and remain eligible for retry. The transport uses Codex authentication through the experimental ChatGPT apps gateway.

## Work with the archive

`notcrawl tui` opens a three-pane terminal browser for workspaces, teamspaces, pages, and databases. It supports keyboard and mouse navigation, filtering, sorting, local refresh, opening or copying the selected Notion URL, and local/remote state in the footer.

Database rows can be exported separately from the Markdown archive:

```sh
notcrawl databases
notcrawl export-db --database DATABASE_ID --format csv --output roadmap.csv
notcrawl export-db --all --dir exports/csv
```

The main commands are:

| Command | Purpose |
|---|---|
| `init` | Write a starter config |
| `doctor` | Check config, SQLite, the desktop cache, and token presence |
| `status` | Show archive counts, last sync time, and database/WAL size |
| `report` | Summarize recent page, database, space, and comment activity |
| `maintain` | Rebuild FTS, optimize indexes, and optionally run `VACUUM` |
| `sync` | Ingest `desktop`, `api`, `notion-mcp`, or all enabled sources |
| `tap` | Alias for `sync --source desktop` |
| `export-md` | Render normalized Markdown from SQLite |
| `databases` / `export-db` | List and export crawled Notion databases |
| `search` | Search page and comment text through FTS5 |
| `tui` | Browse archived pages and databases |
| `sql` | Run read-only SQL against the archive |
| `publish` | Export SQLite tables and Markdown into a git share repository |
| `subscribe` / `update` | Merge current or historical git share snapshots |
| `metadata` / `status --json` / `doctor --json` | Emit crawlkit control data for automation |

Run `notcrawl --help` for the full command summary.

`sql` accepts one SELECT, WITH, or PRAGMA statement and opens the archive read-only. Quoted punctuation and SQL comments are supported. It never creates or migrates the archive; run `sync` first to prepare a new archive or upgrade an older schema.

## Share an archive

Git share mode publishes compressed JSONL table snapshots and normalized Markdown. Another machine can subscribe and search the archive without Notion credentials.

`publish --tag NAME` creates an immutable checkpoint. `subscribe` and `update` merge snapshots without deleting local-only rows by default; `--restore` requests exact replacement, and `--retain-revisions` saves replaced local payloads.

Secrets are not included in Markdown or git share snapshots.

## Configuration

`notcrawl init` writes `~/.notcrawl/config.toml`. The default data paths are:

| Data | Path |
|---|---|
| SQLite archive | `~/.notcrawl/notcrawl.db` |
| Desktop snapshots | `~/.notcrawl/cache` |
| Markdown archive | `~/.notcrawl/pages` |
| Git share checkout | `~/.notcrawl/share` |

See [`config.example.toml`](config.example.toml) for every setting.

Interactive terminal runs check for a newer release once per day. `notcrawl check-update` checks immediately; set `NOTCRAWL_NO_UPDATE_CHECK=1` or `CRAWLKIT_NO_UPDATE_CHECK=1` to disable the passive check.

## Safety model

Desktop mode is read-only. It snapshots Notion's local SQLite database before reading it and never writes to Notion application storage. Cache coverage is opportunistic, so missing rows are not treated as deletions; explicit Notion tombstones still retire records. Markdown marks pages whose bodies were not cached so the API or MCP source can fill them later.

API mode uses the official Notion API and stores raw payloads alongside normalized rows so exports can improve without another crawl.

Notion MCP mode is read-only and targeted. It reads the Codex bearer credential at request time, never stores it, resolves only the connected Notion search and fetch tools, and strips signed URL credentials before persisting connector Markdown. Credentials are sent only to the configured HTTPS ChatGPT apps gateway. The gateway and Codex auth-file format are experimental contracts.

## Architecture

`notcrawl` uses [`crawlkit`](https://github.com/openclaw/crawlkit) for config paths, SQLite helpers, snapshot packing and import, git-backed sharing, output formatting, status payloads, and the terminal explorer. Notion API and Desktop parsing, schemas, Markdown rendering, and FTS content remain in this repository.

See [`SPEC.md`](SPEC.md) for the data model and archive contracts. Maintainers can find release packaging and verification in [`docs/distribution.md`](docs/distribution.md).

## Development

Go 1.27.1 or newer is required.

```sh
make build
make test
make check
```

`make check` runs the dependency, formatting, vet, dead-code, test, smoke, release-config, and snapshot gates used by CI. See [`CONTRIBUTING.md`](CONTRIBUTING.md) before sending a change.

## License

MIT. See [`LICENSE`](LICENSE).
