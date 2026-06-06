package notiondesktop

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/notcrawl/internal/store"
	_ "modernc.org/sqlite"
)

func TestPruneDesktopSnapshotsKeepsNewestAndSidecars(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"notion-desktop-1000.db",
		"notion-desktop-2000.db",
		"notion-desktop-3000.db",
	}
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.WriteFile(path+suffix, []byte(suffix), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		modTime := time.Unix(int64(i+1), 0)
		for _, target := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Chtimes(target, modTime, modTime); err != nil {
				t.Fatal(err)
			}
		}
	}

	current := filepath.Join(dir, "notion-desktop-3000.db")
	if err := pruneDesktopSnapshots(dir, 2, current); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"notion-desktop-2000.db", "notion-desktop-3000.db"} {
		path := filepath.Join(dir, name)
		for _, target := range []string{path, path + "-wal", path + "-shm"} {
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("expected %s to remain: %v", target, err)
			}
		}
	}
	for _, target := range []string{
		filepath.Join(dir, "notion-desktop-1000.db"),
		filepath.Join(dir, "notion-desktop-1000.db-wal"),
		filepath.Join(dir, "notion-desktop-1000.db-shm"),
	} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be pruned, got %v", target, err)
		}
	}
}

func TestIngestBlocksDerivesUntitledPageFromChildText(t *testing.T) {
	ctx := context.Background()
	src, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.ExecContext(ctx, `create table block (
		id text primary key,
		space_id text,
		type text,
		properties text,
		content text,
		collection_id text,
		created_time integer,
		last_edited_time integer,
		parent_id text,
		parent_table text,
		alive integer,
		format text
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ExecContext(ctx, `insert into block(id, space_id, type, properties, content, collection_id, created_time, last_edited_time, parent_id, parent_table, alive, format)
		values
		('page1', 'space1', 'page', '{}', '', '', 1, 1, '', '', 1, ''),
		('child1', 'space1', 'text', '{"title":[["Decision log"]]}', '', '', 2, 2, 'page1', 'block', 1, '')`); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, _, err := ingestBlocks(ctx, st, src, 1); err != nil {
		t.Fatal(err)
	}

	var title string
	if err := st.DB().QueryRowContext(ctx, `select title from pages where id = 'page1'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Decision log" {
		t.Fatalf("expected child text title, got %q", title)
	}
}

func TestIngestBlocksPreservesRowsMissingFromLatestSnapshot(t *testing.T) {
	ctx := context.Background()
	src, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.ExecContext(ctx, `create table block (
		id text primary key,
		space_id text,
		type text,
		properties text,
		content text,
		collection_id text,
		created_time integer,
		last_edited_time integer,
		parent_id text,
		parent_table text,
		alive integer,
		format text
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ExecContext(ctx, `insert into block(id, space_id, type, properties, content, collection_id, created_time, last_edited_time, parent_id, parent_table, alive, format)
		values
		('page1', 'space1', 'page', '{"title":[["Plan"]]}', '["child1"]', '', 1, 1, '', '', 1, ''),
		('child1', 'space1', 'text', '{"title":[["Cached body"]]}', '', '', 2, 2, 'page1', 'block', 1, ''),
		('page2', 'space1', 'page', '{"title":[["Evicted page"]]}', '', '', 3, 3, '', '', 1, ''),
		('page3', 'space1', 'page', '{"title":[["API page"]]}', '["child3"]', '', 4, 4, '', '', 1, ''),
		('child3', 'space1', 'text', '{"title":[["API body"]]}', '', '', 5, 5, 'page3', 'block', 1, '')`); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, _, err := ingestBlocks(ctx, st, src, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Archived API payload", Alive: false, Source: "api", SyncedAt: 9}); err != nil {
		t.Fatal(err)
	}
	var title, source string
	var alive int
	if err := st.DB().QueryRowContext(ctx, `select title, source, alive from pages where id = 'page1'`).Scan(&title, &source, &alive); err != nil {
		t.Fatal(err)
	}
	if title != "Plan" || source != "desktop" || alive != 1 {
		t.Fatalf("dead API payload replaced live Desktop page: title=%q source=%q alive=%d", title, source, alive)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page3", Title: "API page", Alive: true, Source: "api", SyncedAt: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "child3", PageID: "page3", ParentID: "page3", Type: "paragraph", Text: "API body", Alive: true, Source: "api", SyncedAt: 10}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ingestBlocks(ctx, st, src, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ExecContext(ctx, `delete from block where id in ('child1', 'page2', 'page3', 'child3')`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ingestBlocks(ctx, st, src, 3); err != nil {
		t.Fatal(err)
	}

	if err := st.DB().QueryRowContext(ctx, `select alive from blocks where id = 'child1'`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 1 {
		t.Fatalf("evicted child was retired: %d", alive)
	}
	var rawRows int
	if err := st.DB().QueryRowContext(ctx, `select count(*) from raw_records where source = 'desktop' and record_id = 'child1'`).Scan(&rawRows); err != nil {
		t.Fatal(err)
	}
	if rawRows != 1 {
		t.Fatalf("evicted child raw payload was removed: %d", rawRows)
	}
	coverage, err := st.PageBlockCoverage(ctx, "page1")
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Referenced != 1 || coverage.Missing != 0 {
		t.Fatalf("unexpected coverage after eviction: %+v", coverage)
	}
	if err := st.DB().QueryRowContext(ctx, `select alive from pages where id = 'page2'`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 1 {
		t.Fatalf("evicted page was retired: %d", alive)
	}
	var ftsRows int
	if err := st.DB().QueryRowContext(ctx, `select count(*) from page_fts where page_id = 'page2'`).Scan(&ftsRows); err != nil {
		t.Fatal(err)
	}
	if ftsRows != 1 {
		t.Fatalf("evicted page was removed from FTS: %d", ftsRows)
	}
	if err := st.DB().QueryRowContext(ctx, `select alive from pages where id = 'page3'`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 1 {
		t.Fatalf("API-backed page was retired: %d", alive)
	}
	if err := st.DB().QueryRowContext(ctx, `select alive from blocks where id = 'child3'`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 1 {
		t.Fatalf("API-backed block was retired: %d", alive)
	}
	if err := st.DB().QueryRowContext(ctx, `select title, source from pages where id = 'page3'`).Scan(&title, &source); err != nil {
		t.Fatal(err)
	}
	if title != "API page" || source != "api" {
		t.Fatalf("API page payload was overwritten: title=%q source=%q", title, source)
	}
	var text string
	if err := st.DB().QueryRowContext(ctx, `select text, source from blocks where id = 'child3'`).Scan(&text, &source); err != nil {
		t.Fatal(err)
	}
	if text != "API body" || source != "api" {
		t.Fatalf("API block payload was overwritten: text=%q source=%q", text, source)
	}
}

func TestIngestBlocksPreservesPreviousSnapshotWhenCacheQueryIsEmpty(t *testing.T) {
	ctx := context.Background()
	src, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.ExecContext(ctx, `create table block (
		id text,
		space_id text,
		type text,
		properties text,
		content text,
		collection_id text,
		created_time integer,
		last_edited_time integer,
		parent_id text,
		parent_table text,
		alive integer,
		format text
	)`); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Keep me", Alive: true, Source: SourceName, SyncedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ingestBlocks(ctx, st, src, 2); err != nil {
		t.Fatal(err)
	}
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "Keep me" {
		t.Fatalf("empty cache query retired previous snapshot: %#v", pages)
	}
}

func TestIngestCommentsTreatsEmptyCacheAsNonAuthoritative(t *testing.T) {
	ctx := context.Background()
	src, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.ExecContext(ctx, `create table comment (
		id text,
		parent_id text,
		space_id text,
		text text,
		content text,
		created_by_id text,
		created_time integer,
		last_edited_time integer,
		alive integer
	)`); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertComment(ctx, store.Comment{ID: "comment1", PageID: "page1", Text: "Keep me", Alive: true, Source: SourceName, SyncedAt: 1}); err != nil {
		t.Fatal(err)
	}
	count, err := ingestComments(ctx, st, src, 2)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("empty comment cache reported count=%d", count)
	}
	comments, err := st.PageComments(ctx, "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Text != "Keep me" {
		t.Fatalf("empty comment cache altered previous snapshot: %#v", comments)
	}
}
