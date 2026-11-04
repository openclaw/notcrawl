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
