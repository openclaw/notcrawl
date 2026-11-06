# Test cleanup ledger

## Audit boundary

Read all 38 Go test files, the synthetic workspace SQL fixture, the release-script
test runner, and built-CLI smoke recipe at the pinned audit revision. Inspect
owning code where needed to distinguish duplicate setup from distinct behavior.
This closes this bounded notcrawl pass; other crawl-app audits remain separate.

Fixtures use temporary archives, loopback HTTP, synthetic connector credentials
and local Git repositories. Keep `internal/notiondesktop/testdata/workspaces.sql`:
workspace ingestion and CLI progress both consume it. No other tracked testdata
or fixture files remain in this inventory.

## Pagination cleanup

Remove the standalone users repeated-cursor test and its private pager from
`internal/notionapi/api_test.go`. The unchanged `TestAPIPagination/users/repeat`
case exercises the same client path, requires the same repeated-cursor error,
and checks exactly two requests. Also remove the now-unused formatting import:
39 test lines removed, with no production change.

Keep long listings, cursor cycles, opaque cursor preservation, all seven API
endpoints, per-parent block cursors, and failed-walk archive preservation.
Keep direct cursor helper checks for empty cursors and completed listings; these
assert contracts beyond the removed duplicate.

Paired race-enabled comparisons retain healthy success and detection of a lost
repeated-cursor diagnostic and prematurely stopped users pagination. Temporary
Go overlays leave tracked production files unchanged. Full local gates,
independent review and exact-head platform CI are required before landing.

## Earlier landed work

| Scope | Result | PR |
| --- | --- | --- |
| Teamspace paths | Three real exporter cases replace duplicate resolver fixtures; preserve path and front matter. | [202](https://github.com/openclaw/notcrawl/pull/202) |
| Smoke output | Consume complete captured output to avoid broken pipes; retain all six patterns. | [207](https://github.com/openclaw/notcrawl/pull/207) |
| Response size | Share one oversized response rejection; retain exact-limit acceptance and one-request check. | [209](https://github.com/openclaw/notcrawl/pull/209) |
| HTTP callbacks | Return explicit failure responses after nonfatal request assertions in server goroutines. | [214](https://github.com/openclaw/notcrawl/pull/214) |
| Export order | Require both exported blocks before comparing positions. | [217](https://github.com/openclaw/notcrawl/pull/217) |
| Metadata | Require advertised plain-text commands to exist before checking their JSON flags. | [218](https://github.com/openclaw/notcrawl/pull/218) |
| Gateway cancellation | Cancel at the controlled retry response; retain cause, call count and real-server retry controls. | [223](https://github.com/openclaw/notcrawl/pull/223) |
| MCP discovery | Share seven connector/query cases while preserving names, orders, properties, body and source. | [224](https://github.com/openclaw/notcrawl/pull/224) |
| Reports | Compare all window labels, cutoffs, page, collection and comment counts. | [225](https://github.com/openclaw/notcrawl/pull/225) |
| Desktop export | Use independent column expectations and distinct signed schema URLs; retain exact numbers and recovery bytes. | [226](https://github.com/openclaw/notcrawl/pull/226) |
| Snapshot rejection | Share four invalid snapshot fixtures; check intended failures and unchanged destination rows. | [227](https://github.com/openclaw/notcrawl/pull/227) |
| Markdown | Share property setup; remove repeated to-do render; require exact fallback escaping and safe code fences. | [228](https://github.com/openclaw/notcrawl/pull/228) |
| Desktop cache | Share identical cache setup; retain distinct empty schemas and verify workspace collection names. | [229](https://github.com/openclaw/notcrawl/pull/229) |
| Search | Require relevance before recency, preserving equal-rank ordering. | [230](https://github.com/openclaw/notcrawl/pull/230) |
| CLI progress | Use synthetic Desktop input; require exact counts, searchable content and unchanged source bytes. | [231](https://github.com/openclaw/notcrawl/pull/231) |
| API tables | Require both complete table/row mappings and searchable text. | [232](https://github.com/openclaw/notcrawl/pull/232) |
| Database index | Compare complete exported index records, escaped names, headers and generated files. | [233](https://github.com/openclaw/notcrawl/pull/233) |
| Comment retries | Separate status, explicit retry flag and HTML gateway cases; retain exact attempts and stored results. | [234](https://github.com/openclaw/notcrawl/pull/234) |

## Retained contracts and limits

- Keep parsing, normalized storage, FTS, Markdown and CLI integration checks
  separate where they cross different boundaries. Shared sample text alone does
  not establish duplication.
- Keep authoritative API retirement, malformed/partial discovery, rollback,
  archived-page fallback and snapshot merge/restore cases. Desktop cache eviction
  is non-authoritative; its absence and tombstone cases are distinct.
- Keep MCP tool selection, repair invalidation, empty-body preservation, retry
  budgets, cancellation, initialization, URL sanitization and credential routing.
- Keep Desktop envelope decoding, exact integers, signed-capability sanitization,
  untouched recovery payloads, and malformed-known-column rejection.
- Keep Markdown completeness warnings, tree/display order, title/property output,
  generated-only pruning, code whitespace/fences and attachment links.
- Keep CLI read-only SQL, legacy archive inspection, non-mutating TUI JSON,
  output close failures, help side effects and snapshot integer round trips.
- Keep snapshot validation, provenance priority, optional revisions, exact numeric
  conversion, generated-only Git commits and historical update behavior.
- Keep config precedence, release-check timeout, SQLite privacy/pragmas, release
  wrapper refusal checks and built-CLI smoke.

These are synthetic unit/integration and built-CLI checks. They do not establish
live Notion API, installed Desktop, authenticated connector or interactive
terminal E2E coverage. No live archives, credentials, releases or deployments
are part of this pass.
