---
name: notcrawl
description: Use for local Notion archive search, sync freshness, Markdown/database exports, git-share snapshots, and Notcrawl repo/release work.
---

# Notcrawl

Use local archive data first for Notion questions. Browse or hit the Notion API
only when the archive is stale, missing the requested scope, or the user asks
for current external context.

## Sources

- DB: `~/.notcrawl/notcrawl.db`
- Config: `~/.notcrawl/config.toml`
- Cache: `~/.notcrawl/cache`
- Markdown archive: `~/.notcrawl/pages`
- Git share repo: `~/.notcrawl/share`
- Repo: the local `notcrawl` checkout
- Preferred CLI: `notcrawl`; fallback to `go run ./cmd/notcrawl` from the repo if the installed binary is stale

## Freshness

For recent/current questions, check freshness before analysis:

```bash
sqlite3 ~/.notcrawl/notcrawl.db \
  "select coalesce(max(synced_at), 0) from sync_state;"
```

Routine refresh:

```bash
notcrawl doctor
notcrawl sync --source desktop
```

API refresh:

```bash
notcrawl sync --source api
```

Use `notcrawl sync --source all` only when both desktop and API sources are
configured and the broader refresh is intentional.

## Query Workflow

1. Resolve scope: workspace, teamspace, page, database, author, keyword, or date range.
2. Check freshness for recent/current requests.
3. Use CLI for normal reads; use read-only SQL for precise counts/rankings.
4. Report absolute date spans, counts, page/database titles, and known gaps.

Common commands:

```bash
notcrawl search "query"
notcrawl databases
notcrawl report
notcrawl sql "select count(*) from pages;"
```

## SQL

Use `notcrawl sql` for exact counts, joins, and database/page inventory queries
when normal CLI reads are too coarse. The command only allows read-only
`select`, `with`, and `pragma` queries.

Useful examples:

```bash
notcrawl sql "select count(*) as pages from pages;"
notcrawl sql "select parent_table, count(*) as pages from pages group by parent_table order by pages desc;"
notcrawl sql "select title, last_edited_time from pages order by coalesce(last_edited_time, created_time, 0) desc limit 20;"
```

Do not use SQL to mutate the archive.

When the installed CLI lacks a new feature, build or run from the repository
checkout before concluding the feature is missing.

## Notion Boundaries

Desktop mode snapshots the local Notion SQLite database read-only and must not
write to Notion application storage. API mode requires `NOTION_TOKEN`; do not
invent token availability. Git-share snapshots and Markdown exports must not
include secrets.

## Verification

For repo edits, prefer existing Go gates:

```bash
GOWORK=off go test ./...
```

Then run targeted CLI smoke for the touched surface, for example:

```bash
notcrawl doctor
notcrawl status --json
notcrawl search "test"
```
