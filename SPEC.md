# notcrawl Spec

## Goals

- build a local-first Notion crawler
- mirror Notion pages, blocks, databases, comments, and workspace metadata
- store normalized records in SQLite
- preserve raw source records for future re-rendering
- render normalized Markdown blobs into an organized file tree
- support fast text search and raw SQL
- support one-shot backfill and incremental repair
- publish and subscribe private git-backed snapshots

## Product Summary

`notcrawl` is a Go CLI that turns Notion workspace memory into a local
SQLite archive plus normalized Markdown files.

V1 scope:

- macOS Notion Desktop cache discovery
- read-only desktop snapshot ingestion
- official Notion API sync
- targeted repair through a preconfigured Codex Notion MCP app
- pages and blocks
- databases/data sources as collections, including current data-source API endpoints
- database rows as pages linked to their collection
- comments and discussions where available
- users and spaces/workspaces
- FTS5 search over rendered page/comment text
- raw SQL access
- archive status, activity reporting, and SQLite maintenance commands
- Markdown export
- CSV/TSV export for database rows
- git-backed archive publishing and subscription

Out of scope for V1:

- write-back actions
- modifying Notion local storage
- bypassing workspace permissions
- full attachment blob mirroring by default
- public integration Marketplace hardening

## Data Sources

### Desktop Source

Default macOS path:

```text
~/Library/Application Support/Notion/notion.db
```

Desktop sync must:

1. locate Notion Desktop storage
2. snapshot `notion.db` into the cache dir
3. open the snapshot read-only
4. ingest supported tables into the local archive
5. record unsupported source records in `raw_records`

Desktop cache coverage is opportunistic. It only includes what Notion has
cached, downloaded, or recently touched locally.

### API Source

API sync uses `NOTION_TOKEN` by default. It must:

1. search/list pages and data sources visible to the integration
2. recursively fetch block children
3. fetch users
4. fetch comments where the integration has access
5. obey `Retry-After` on rate limits
6. store raw JSON plus normalized rows

New configs should use the current Notion API version. Existing configs pinned
to legacy `2022-06-28` must continue using deprecated database query endpoints.

### Notion MCP Source

Notion MCP sync uses the Notion app already connected in Codex through the
ChatGPT apps gateway. It must:

1. read Codex auth at request time without persisting or logging the bearer token
2. dynamically resolve read-only Notion `search` and `fetch` tools
3. fetch explicit page IDs/URLs or bounded targeted search results
4. automatically repair incomplete known Desktop pages and API pages whose block sync did not complete
5. preserve connector enhanced Markdown, including properties, without lossy block reconstruction
6. strip signed URL credentials before storing or exporting content
7. avoid claiming complete workspace enumeration or incremental coverage

The Codex gateway transport is experimental and OpenAI-specific. Official API
sync remains the stable integration path.

## SQLite Archive

SQLite is canonical. Markdown is generated output.

Store startup must enable WAL, foreign keys, a busy timeout, normal
synchronous writes, in-memory temp storage, and the crawler query indexes needed
for common page, collection, comment, raw-record, and sync-state lookups.

`report` must provide a SQL-free archive summary: total records, recent edited
page/comment windows, top databases, top spaces, and recently edited pages.

Core tables:

- `spaces`
- `users`
- `teams`
- `pages`
- `blocks`
- `collections`
- `collection_views`
- `comments`
- `discussions`
- `raw_records`
- `sync_state`
- `page_fts`
- `comment_fts`

## Markdown Archive

Markdown export writes deterministic Unicode-safe paths. Path components keep
readable letters, numbers, CJK text, and emoji while replacing filesystem path
separators and unsafe punctuation with dashes:

```text
pages/<space-slug>/<team-slug>/<page-title>-<short-id>.md
```

The team slug is omitted when no teamspace can be resolved.

Each export removes stale generated `.md` files under the Markdown root while
leaving non-Markdown sidecar files alone.

Each file starts with YAML-ish front matter:

```yaml
---
id: ...
space_id: ...
title: ...
source: desktop+api
notion_url: ...
created_time: ...
last_edited_time: ...
---
```

The body renders blocks into normalized Markdown. Unsupported blocks should be
represented with concise placeholders, not silently dropped.

## Git Share

Git share mode exports:

```text
manifest.json
data/*.jsonl.gz
pages/**/*.md
```

`publish` writes a snapshot and optionally commits/pushes it. `--tag` attaches
an immutable Git checkpoint to the resulting commit.

`subscribe` clones a snapshot repo, writes reader config, and imports data into
SQLite without requiring Notion credentials.

`update` pulls the latest snapshot and imports it. `update --ref REF` reads the
manifest and table objects at a tag, commit, or branch without changing the
share checkout.

## Database Export

API sync discovers databases/data sources visible to the integration, stores
metadata in `collections`, queries each collection for row pages, and links
those pages through `pages.collection_id`.

`export-db` renders row properties into delimited text:

```text
notcrawl export-db --database <database-id> --format csv --output rows.csv
notcrawl export-db --database <database-id> --format tsv --output rows.tsv
notcrawl export-db --all --dir exports/csv
```

The first columns are stable metadata:

- `page_id`
- `page_title`
- `url`

Remaining columns come from the database schema, with any extra row properties
appended alphabetically.
