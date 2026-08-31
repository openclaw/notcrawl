package share

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/store"
)

func TestPublishAndImportSnapshot(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	now := store.NowMS()
	if err := src.UpsertPage(ctx, store.Page{ID: "page1", Title: "Launch", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := src.UpsertBlock(ctx, store.Block{ID: "block1", PageID: "page1", ParentID: "page1", Type: "text", Text: "hello", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	mdDir := t.TempDir()
	if _, err := (markdown.Exporter{Store: src, Dir: mdDir}).Export(ctx); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	s, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Manifest.Tables) == 0 {
		t.Fatal("expected tables in manifest")
	}
	if _, err := os.Stat(filepath.Join(repo, "pages", "default", "launch-page1.md")); err != nil {
		t.Fatal(err)
	}
	for _, table := range s.Manifest.Tables {
		if table.Name == "record_sources" {
			t.Fatal("record_sources must remain outside the legacy tables list")
		}
	}
	if s.Manifest.RecordSources == nil || s.Manifest.RecordSources.Name != "record_sources" {
		t.Fatal("expected source provenance sidecar")
	}
	stalePage := filepath.Join(repo, "pages", "default", "stale.md")
	if err := os.WriteFile(stalePage, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	pageSidecar := filepath.Join(repo, "pages", "default", "README.txt")
	if err := os.WriteFile(pageSidecar, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleData := filepath.Join(repo, "data", "stale.jsonl.gz")
	if err := os.WriteFile(staleData, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataSidecar := filepath.Join(repo, "data", "README.txt")
	if err := os.WriteFile(dataSidecar, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{stalePage, staleData} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected generated stale file %s to be pruned, got %v", path, err)
		}
	}
	for _, path := range []string{pageSidecar, dataSidecar} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected sidecar %s to remain: %v", path, err)
		}
	}
	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, dst, repo); err != nil {
		t.Fatal(err)
	}
	results, err := dst.Search(ctx, "hello", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected imported search result, got %d", len(results))
	}
	hasSource, err := dst.RecordHasLiveSource(ctx, "page", "page1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !hasSource {
		t.Fatal("expected imported page source provenance")
	}
}

func TestPublishAndImportPreservesMixedSourceProvenance(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	now := store.NowMS()
	if err := src.UpsertPage(ctx, store.Page{ID: "page1", Title: "Desktop", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := src.UpsertPage(ctx, store.Page{ID: "page1", Title: "API", Alive: true, Source: "api", SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	if err := src.UpsertPage(ctx, store.Page{ID: "page1", Title: "Archived", Alive: false, Source: "api", SyncedAt: now + 2}); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, dst, repo); err != nil {
		t.Fatal(err)
	}
	desktopLive, err := dst.RecordHasLiveSource(ctx, "page", "page1", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	apiLive, err := dst.RecordHasLiveSource(ctx, "page", "page1", "api")
	if err != nil {
		t.Fatal(err)
	}
	if !desktopLive || apiLive {
		t.Fatalf("source provenance after import: desktop=%v api=%v", desktopLive, apiLive)
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "Desktop" || pages[0].Source != "desktop" {
		t.Fatalf("canonical payload after import = %#v", pages)
	}
}

func TestImportLegacySnapshotRebuildsSourceProvenance(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Launch", "hello")
	defer src.Close()
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repo, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.RecordSources = nil
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "data", "record_sources.jsonl.gz")); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.UpsertPage(ctx, store.Page{ID: "stale", Title: "Stale destination row", Alive: true, Source: "desktop", SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, dst, repo); err != nil {
		t.Fatal(err)
	}
	hasSource, err := dst.RecordHasLiveSource(ctx, "page", "page1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !hasSource {
		t.Fatal("expected legacy provenance rebuild")
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("legacy merge removed destination rows: %#v", pages)
	}
	seen := map[string]bool{}
	for _, page := range pages {
		seen[page.ID] = true
	}
	if !seen["page1"] || !seen["stale"] {
		t.Fatalf("legacy merge pages = %#v", pages)
	}
}

func TestImportRestoreReplacesDestinationAndTombstoneState(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Remote live", "remote body")
	defer src.Close()
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	now := store.NowMS()
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Local deleted", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Local deleted", Alive: false, Source: "test", SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertPage(ctx, store.Page{ID: "local-only", Title: "Local only", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWithOptions(ctx, dst, repo, ImportOptions{Restore: true, RetainRevisions: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "restore" || result.Revisions != 4 {
		t.Fatalf("import result = %+v", result)
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "page1" || pages[0].Title != "Remote live" || !pages[0].Alive {
		t.Fatalf("restored pages = %#v", pages)
	}
	exists, alive, err := dst.RecordSourceState(ctx, "page", "page1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || !alive {
		t.Fatalf("restored source state: exists=%v alive=%v", exists, alive)
	}
	var removedRevision string
	if err := dst.DB().QueryRowContext(ctx, `select payload_json from record_revisions
		where record_table = 'pages' and record_key like '%local-only%' and reason = 'snapshot-restore'`).Scan(&removedRevision); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(removedRevision, "Local only") {
		t.Fatalf("restore revision payload = %s", removedRevision)
	}
}

func TestImportMergePreservesLocalRowsAndTombstonesWithOptionalRevisions(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Remote live", "remote body")
	defer src.Close()
	if err := src.UpsertSpace(ctx, store.Space{ID: "space1", Name: "Remote space", RawJSON: `{"name":"remote"}`, Source: "test", SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	now := store.NowMS()
	if err := dst.UpsertSpace(ctx, store.Space{ID: "space1", Name: "Local space", RawJSON: `{"name":"local"}`, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertSpace(ctx, store.Space{ID: "rowid-sentinel", Name: "Sentinel", Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Local deleted", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Local deleted", Alive: false, Source: "test", SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertPage(ctx, store.Page{ID: "local-only", Title: "Local only", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	var spaceRowID int64
	if err := dst.DB().QueryRowContext(ctx, `select rowid from spaces where id = 'space1'`).Scan(&spaceRowID); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWithOptions(ctx, dst, repo, ImportOptions{RetainRevisions: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "merge" || result.Revisions != 1 {
		t.Fatalf("import result = %+v", result)
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "local-only" || pages[0].Title != "Local only" {
		t.Fatalf("merge pages = %#v", pages)
	}
	var tombstonedTitle string
	var tombstonedAlive int
	if err := dst.DB().QueryRowContext(ctx, `select title, alive from pages where id = 'page1'`).Scan(&tombstonedTitle, &tombstonedAlive); err != nil {
		t.Fatal(err)
	}
	if tombstonedAlive != 0 || tombstonedTitle != "Local deleted" {
		t.Fatalf("local tombstone was overwritten: title=%q alive=%d", tombstonedTitle, tombstonedAlive)
	}
	var deletedAt int64
	var deletionSource, deletionReason string
	if err := dst.DB().QueryRowContext(ctx, `select deleted_at, deletion_source, deletion_reason
		from record_sources where record_table = 'page' and record_id = 'page1' and source = 'test'`).Scan(
		&deletedAt, &deletionSource, &deletionReason); err != nil {
		t.Fatal(err)
	}
	if deletedAt != now+1 || deletionSource != "test" || deletionReason != "explicit-source-delete" {
		t.Fatalf("tombstone metadata = %d %q %q", deletedAt, deletionSource, deletionReason)
	}
	var spaceName string
	if err := dst.DB().QueryRowContext(ctx, `select name from spaces where id = 'space1'`).Scan(&spaceName); err != nil {
		t.Fatal(err)
	}
	if spaceName != "Remote space" {
		t.Fatalf("merged space name = %q", spaceName)
	}
	var mergedSpaceRowID int64
	if err := dst.DB().QueryRowContext(ctx, `select rowid from spaces where id = 'space1'`).Scan(&mergedSpaceRowID); err != nil {
		t.Fatal(err)
	}
	if mergedSpaceRowID != spaceRowID {
		t.Fatalf("merge replaced stable space rowid: before=%d after=%d", spaceRowID, mergedSpaceRowID)
	}
	var revisionPayload string
	if err := dst.DB().QueryRowContext(ctx, `select payload_json from record_revisions
		where record_table = 'spaces' and reason = 'snapshot-merge'`).Scan(&revisionPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(revisionPayload, "Local space") {
		t.Fatalf("revision payload = %s", revisionPayload)
	}
}

func TestImportMergeAppliesIncomingTombstone(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	now := store.NowMS()
	if err := src.UpsertPage(ctx, store.Page{ID: "page1", Title: "Remote deleted", Alive: true, Source: "api", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := src.UpsertPage(ctx, store.Page{ID: "page1", Title: "Remote deleted", Alive: false, Source: "api", SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Local live", Alive: true, Source: "api", SyncedAt: now - 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, dst, repo); err != nil {
		t.Fatal(err)
	}
	var alive int
	if err := dst.DB().QueryRowContext(ctx, `select alive from pages where id = 'page1'`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 0 {
		t.Fatalf("incoming tombstone alive = %d", alive)
	}
	var reason string
	if err := dst.DB().QueryRowContext(ctx, `select deletion_reason from record_sources
		where record_table = 'page' and record_id = 'page1' and source = 'api'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "explicit-source-delete" {
		t.Fatalf("incoming deletion reason = %q", reason)
	}
}

func TestImportMergePreservesHigherPriorityLocalPayloadAndRemoteFallback(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Remote fallback", "remote body")
	defer src.Close()
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}
	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	now := store.NowMS()
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Local API", Alive: true, Source: "api", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	result, err := ImportWithOptions(ctx, dst, repo, ImportOptions{RetainRevisions: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revisions != 0 {
		t.Fatalf("unchanged canonical payload produced revisions: %+v", result)
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "Local API" || pages[0].Source != "api" {
		t.Fatalf("merged canonical page = %#v", pages)
	}
	if err := dst.UpsertPage(ctx, store.Page{ID: "page1", Title: "Deleted API", Alive: false, Source: "api", SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	pages, err = dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "Remote fallback" || pages[0].Source != "test" {
		t.Fatalf("promoted imported fallback = %#v", pages)
	}
}

func TestImportRejectsIncompleteManifestBeforeClearingDestination(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Launch", "hello")
	defer src.Close()
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repo, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var tables []TableManifest
	for _, table := range manifest.Tables {
		if table.Name != "pages" {
			tables = append(tables, table)
		}
	}
	manifest.Tables = tables
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.UpsertPage(ctx, store.Page{ID: "keep", Title: "Keep destination", Alive: true, Source: "desktop", SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, dst, repo); err == nil {
		t.Fatal("expected incomplete manifest rejection")
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "keep" {
		t.Fatalf("incomplete import altered destination: %#v", pages)
	}
}

func TestImportRejectsRowCountMismatchBeforeCommit(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Launch", "hello")
	defer src.Close()
	repo := t.TempDir()
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repo, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Tables {
		if manifest.Tables[i].Name == "pages" {
			manifest.Tables[i].Rows++
		}
	}
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.UpsertPage(ctx, store.Page{ID: "keep", Title: "Keep destination", Alive: true, Source: "desktop", SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, dst, repo); err == nil {
		t.Fatal("expected row count mismatch rejection")
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "keep" {
		t.Fatalf("row count mismatch altered destination: %#v", pages)
	}
}

func TestImportRejectsSymlinkedSnapshotFile(t *testing.T) {
	ctx := context.Background()
	src, mdDir := snapshotStoreForTest(t, ctx, "Launch", "hello")
	defer src.Close()
	repo := t.TempDir()
	summary, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir})
	if err != nil {
		t.Fatal(err)
	}
	var pagesPath string
	for _, table := range summary.Manifest.Tables {
		if table.Name == "pages" {
			pagesPath = filepath.Join(repo, table.Path)
			break
		}
	}
	if pagesPath == "" {
		t.Fatal("pages table missing from manifest")
	}
	data, err := os.ReadFile(pagesPath)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "pages.jsonl.gz")
	if err := os.WriteFile(outside, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pagesPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, pagesPath); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.UpsertPage(ctx, store.Page{ID: "keep", Title: "Keep destination", Alive: true, Source: "desktop", SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, dst, repo); err == nil {
		t.Fatal("expected symlinked snapshot rejection")
	}
	pages, err := dst.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "keep" {
		t.Fatalf("symlinked import altered destination: %#v", pages)
	}
}

func TestEnsureRepoUpdatesExistingOrigin(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, repo, "init")
	runGitForTest(t, repo, "remote", "add", "origin", "https://example.invalid/old.git")

	const remote = "https://example.invalid/fresh.git"
	if err := ensureRepo(ctx, repo, remote, "main"); err != nil {
		t.Fatal(err)
	}

	got := gitOutputForTest(t, repo, "remote", "get-url", "origin")
	if strings.TrimSpace(got) != remote {
		t.Fatalf("origin = %q", got)
	}
}

func TestPublishWithoutPushDoesNotFetchRemote(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	runGitForTest(t, t.TempDir(), "init", "-b", "main", repo)
	st, mdDir := snapshotStoreForTest(t, ctx, "Local", "offline snapshot")
	_, err := Publish(ctx, st, PublishOptions{
		RepoPath:    repo,
		Remote:      "https://example.invalid/archive.git",
		Branch:      "main",
		MarkdownDir: mdDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(ctx, st, PublishOptions{
		RepoPath:    repo,
		Remote:      "https://example.invalid/archive.git",
		Branch:      "main",
		MarkdownDir: mdDir,
		Commit:      true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishTagRequiresCommitBeforeWriting(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if _, err := Publish(context.Background(), nil, PublishOptions{RepoPath: repo, Tag: "snapshot/test"}); err == nil {
		t.Fatal("expected tag without commit to fail")
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatalf("failed tagged publish should not create repo: %v", err)
	}
}

func TestPublishValidatesTagBeforeRemoteSync(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	runGitForTest(t, t.TempDir(), "init", "-b", "main", repo)
	st, mdDir := snapshotStoreForTest(t, ctx, "Local", "offline snapshot")
	_, err := Publish(ctx, st, PublishOptions{
		RepoPath:    repo,
		Remote:      "https://example.invalid/archive.git",
		Branch:      "main",
		MarkdownDir: mdDir,
		Commit:      true,
		Push:        true,
		Tag:         "bad tag",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid snapshot tag") {
		t.Fatalf("Publish error = %v", err)
	}
}

type failOnCloseWriter struct {
	io.WriteCloser
	closeErr error
}

func (w failOnCloseWriter) Close() error {
	_ = w.WriteCloser.Close()
	return w.closeErr
}

type failOnWriteCloser struct {
	io.WriteCloser
	writeErr error
}

func (w failOnWriteCloser) Write([]byte) (int, error) {
	return 0, w.writeErr
}

func TestPublishReturnsExportCloseErrorWithoutCommitting(t *testing.T) {
	cases := []struct {
		name string
		err  error
		wrap func(io.WriteCloser, error) io.WriteCloser
	}{
		{"file close", errors.New("disk full"), func(inner io.WriteCloser, err error) io.WriteCloser {
			return failOnCloseWriter{WriteCloser: inner, closeErr: err}
		}},
		{"gzip close", errors.New("gzip footer flush"), func(inner io.WriteCloser, err error) io.WriteCloser {
			return failOnWriteCloser{WriteCloser: inner, writeErr: err}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := filepath.Join(t.TempDir(), "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			runGitForTest(t, repo, "init", "-b", "main")
			runGitForTest(t, repo,
				"-c", "commit.gpgsign=false",
				"-c", "user.name=test",
				"-c", "user.email=test@example.invalid",
				"commit", "--allow-empty", "-m", "seed",
			)
			src, mdDir := snapshotStoreForTest(t, ctx, "Launch", "hello")
			defer src.Close()

			orig := createExportFile
			t.Cleanup(func() { createExportFile = orig })
			createExportFile = func(name string) (io.WriteCloser, error) {
				f, err := os.Create(name)
				if err != nil {
					return nil, err
				}
				return tc.wrap(f, tc.err), nil
			}

			s, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir, Commit: true})
			if err == nil {
				t.Fatal("expected export close error")
			}
			if !errors.Is(err, tc.err) && !strings.Contains(err.Error(), tc.err.Error()) {
				t.Fatalf("Publish error = %v", err)
			}
			if s.Committed {
				t.Fatal("snapshot must not be committed after export close error")
			}
			log := gitOutputForTest(t, repo, "log", "--format=%s")
			if strings.Contains(log, "archive: notcrawl snapshot") {
				t.Fatalf("unexpected snapshot commit:\n%s", log)
			}
		})
	}
}

func TestPublishCommitsOnlyGeneratedSnapshotFiles(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, repo, "init", "-b", "main")
	notes := filepath.Join(repo, "notes.txt")
	if err := os.WriteFile(notes, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, repo, "add", "notes.txt")
	runGitForTest(t, repo,
		"-c", "commit.gpgsign=false",
		"-c", "user.name=test",
		"-c", "user.email=test@example.invalid",
		"commit", "-m", "seed notes",
	)
	if err := os.WriteFile(notes, []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	src, mdDir := snapshotStoreForTest(t, ctx, "Launch", "hello generated")
	defer src.Close()
	s, err := Publish(ctx, src, PublishOptions{RepoPath: repo, MarkdownDir: mdDir, Commit: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Committed {
		t.Fatal("expected generated snapshot commit")
	}
	status := gitOutputForTest(t, repo, "status", "--short", "--", "notes.txt")
	if !strings.HasPrefix(status, " M notes.txt") {
		t.Fatalf("expected unrelated tracked edit to remain unstaged, got %q", status)
	}
	committed := gitOutputForTest(t, repo, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(committed, "notes.txt") {
		t.Fatalf("unexpected unrelated file in snapshot commit:\n%s", committed)
	}
}

func TestUpdatePullsExistingOriginWhenRemoteNotConfigured(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	runGitForTest(t, dir, "init", "--bare", remote)

	seed := filepath.Join(dir, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, seed, "init", "-b", "main")
	src, mdDir := snapshotStoreForTest(t, ctx, "Old", "old snapshot")
	oldSummary, err := Publish(ctx, src, PublishOptions{RepoPath: seed, Remote: remote, MarkdownDir: mdDir, Commit: true, Push: true, Tag: "snapshot/old"})
	if err != nil {
		t.Fatal(err)
	}
	if oldSummary.Tag != "snapshot/old" {
		t.Fatalf("published tag = %q", oldSummary.Tag)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "local")
	runGitForTest(t, dir, "clone", remote, local)

	fresh, freshMD := snapshotStoreForTest(t, ctx, "Fresh", "fresh snapshot")
	if _, err := Publish(ctx, fresh, PublishOptions{RepoPath: seed, Remote: remote, MarkdownDir: freshMD, Commit: true, Push: true, Tag: "snapshot/fresh"}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Update(ctx, dst, "", local, "main"); err != nil {
		t.Fatal(err)
	}
	results, err := dst.Search(ctx, "fresh", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Fresh" {
		t.Fatalf("expected fresh pulled snapshot, got %#v", results)
	}
	currentHead := strings.TrimSpace(gitOutputForTest(t, local, "rev-parse", "HEAD"))
	manifest, resolved, err := UpdateAt(ctx, dst, "", local, "main", "snapshot/old")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == "" || manifest.GeneratedAt == "" {
		t.Fatalf("historical update missing ref or manifest: ref=%q manifest=%+v", resolved, manifest)
	}
	results, err = dst.Search(ctx, "old", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Old" {
		t.Fatalf("expected old historical snapshot, got %#v", results)
	}
	afterHead := strings.TrimSpace(gitOutputForTest(t, local, "rev-parse", "HEAD"))
	if afterHead != currentHead {
		t.Fatalf("historical update changed checkout from %s to %s", currentHead, afterHead)
	}
}

func snapshotStoreForTest(t *testing.T, ctx context.Context, title, text string) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: title, Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "block1", PageID: "page1", ParentID: "page1", Type: "text", Text: text, Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	mdDir := t.TempDir()
	if _, err := (markdown.Exporter{Store: st, Dir: mdDir}).Export(ctx); err != nil {
		t.Fatal(err)
	}
	return st, mdDir
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}
