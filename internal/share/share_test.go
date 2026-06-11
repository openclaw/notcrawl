package share

import (
	"context"
	"encoding/json"
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
	if len(pages) != 1 || pages[0].ID != "page1" {
		t.Fatalf("legacy import retained stale destination rows: %#v", pages)
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
	if _, err := Publish(ctx, src, PublishOptions{RepoPath: seed, MarkdownDir: mdDir, Commit: true}); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, seed, "remote", "add", "origin", remote)
	runGitForTest(t, seed, "push", "-u", "origin", "main")

	local := filepath.Join(dir, "local")
	runGitForTest(t, dir, "clone", remote, local)

	fresh, freshMD := snapshotStoreForTest(t, ctx, "Fresh", "fresh snapshot")
	if _, err := Publish(ctx, fresh, PublishOptions{RepoPath: seed, Remote: remote, MarkdownDir: freshMD, Commit: true, Push: true}); err != nil {
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
