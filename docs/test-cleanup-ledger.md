# Test cleanup ledger

## Markdown teamspace paths

Consolidated four tests into three exporter cases: direct team, collection
parent, and block parent. Removed `internal/markdown/paths_test.go`, whose two
SQLite fixtures repeated parent relationships exercised by the exporter.

The retained cases export a real Markdown file from a temporary archive and
verify the exact workspace/teamspace path plus team ID/name front matter.
Collection and block cases now check metadata as well as resolution; block
parent coverage now reaches the exporter instead of stopping at its resolver.
No production behavior changed. The workspace SQL fixture remains in use by
`TestIngestWorkspaceScope` and must stay.

Validation: focused Markdown tests, full Go suite/static gates, isolated CLI
smoke, structured autoreview, and PR CI including cross-platform tests and
credential-free packaging. No live Notion data, credentials, or releases.

Remaining: finish the semantic test/fixture audit across other packages and
crawl apps; distinct parsing, ingestion, and export layers are not duplicates
merely because they share sample inputs.

## Smoke output checks

The smoke recipe returned success while printing four `Broken pipe` errors on
macOS: `grep -q` closed its input while the shell was still printing the captured
help and JSON. Keep the same six patterns, consume their complete streams, and
redirect matching lines to `/dev/null` so output remains quiet.

Validation runs the actual `make smoke` target and requires success without
broken-pipe diagnostics. The temporary config and SQLite archive remain isolated;
no product commands, assertions, release behavior, or live Notion data changed.

## Official API response size

Removed `TestDoRejectsOversizedSuccessBody`: it sent the same oversized JSON
fixture over loopback HTTP as `TestDoDoesNotRetryOversizedSuccessBody` and
asserted the same rejection. The retained case also checks that the client
issues exactly one request. Moved the fixture-size guard into it and made it
exact: one byte above the limit.

Kept the exact-limit acceptance test and added its exact fixture-size guard,
so the two tests cover opposite sides of the size boundary. Kept the small
JSON decoding and transport-error retry cases; they cover different behavior.

Validation: focused API race tests, full Go suite, static checks, isolated
CLI smoke, independent review, and PR CI. No production or live Notion changes.

## HTTP handler assertion failures

Replaced 21 fatal assertions in Notion API HTTP fixture callbacks with nonfatal
errors, HTTP 400 responses, and immediate returns. Fatal assertions belong in
the test goroutine; these callbacks run in server goroutines and previously
aborted requests with EOF, obscuring the rejected request and triggering retries.

Request conditions, successful responses, temporary SQLite checks, and root-test
fatal assertions remain intact. Validation covers a forced request mismatch
before/after the change, focused race tests, full Go tests/static gates, isolated
CLI smoke, structured review, and PR CI. No live Notion data or releases.

## Display-order assertions require exported blocks

Require both fixture blocks to appear in the exported Markdown in display order.
The old index comparison passed when the first block or both blocks were absent.
Keep the real temporary SQLite archive, exported-file read and block fixtures.

Disposable post-export overlays reproduce both missing-output false greens, then
verify rejection after the fix. Reversed blocks fail before and after; healthy
exports pass under the race detector. Validate full Go/static gates, isolated CLI
smoke, independent review and CI. No production, live Notion data or releases.

## Metadata command presence

Require each expected plain-text command to exist before checking its JSON flag.
The old map lookup accepted missing commands as `false`; independent omissions
of sync, tap, publish, subscribe and update all passed the old test and fail the
repaired test under the race detector. Healthy metadata passes both versions.
Keep the existing fixture, command lists and positive JSON checks unchanged.
Validate full CLI race tests, `make check`, built-CLI metadata, review and platform
CI. No production changes, live Notion data, credentials or releases.

## Gateway cancellation boundary

Replace the cancellation case's loopback server and 25 ms deadline with a
controlled transport returning the same 429 and 60-second retry delay. Cancel
at that response, require the cancellation cause, and count transport calls
so an extra request cannot hide behind a cancelled network context. Keep the
real-server success/retry-limit cases and delay-bound table.

Six before/after full MCP-package race suites: healthy cases pass, skipped
backoff remains detected, and a fabricated deadline error accepted by the old
case now fails. Temporary Go overlays leave production bytes unchanged.
Validate local Go/static/smoke checks, independent review and platform CI with
credential-free packaging. No live Notion data or releases.

## Targeted MCP query fixture consolidation

Fold the standalone query-discovery setup into the connector-selection matrix.
Preserve the default tool list (including its legacy distractor), all three
explicit name pairs, both tool orders and the same query/limit: seven cases.
Share summary/query/page identity/source/properties/body assertions across all
cases. Remove duplicated setup; the test file is fifteen lines shorter.

Ten before/after full MCP-package race suites retain healthy success and
detection of wrong page sources, missing properties/body and wrong tool
precedence. Temporary overlays leave all three production files unchanged.
Require local Go/static/smoke checks, independent review and platform/packaging
CI. Synthetic fixtures only; no live Notion data, product changes or releases.
