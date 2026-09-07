package notiondesktop

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestIngestWorkspaceScope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "desktop.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	fixture, err := os.ReadFile("testdata/workspaces.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(string(fixture)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cacheDir := filepath.Join(dir, "cache")
	scope := []string{" AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "}
	summary, err := Ingest(ctx, st, sourcePath, cacheDir, scope)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Spaces != 1 || summary.Users != 1 || summary.Teams != 1 || summary.Collections != 1 || summary.Pages != 1 || summary.Blocks != 2 || summary.RawRecords != 2 || summary.Comments != 1 {
		t.Fatalf("unexpected scoped ingestion: %+v", summary)
	}
	for _, table := range []string{"spaces", "teams", "collections", "pages", "blocks", "raw_records", "comments"} {
		column := "space_id"
		if table == "spaces" {
			column = "id"
		}
		var count int
		if err := st.DB().QueryRow("select count(*) from "+table+" where "+column+" != ?", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa").Scan(&count); err != nil || count != 0 {
			t.Fatalf("unexpected rows in %s: count=%d err=%v", table, count, err)
		}
	}

	// An empty list restores the existing all-workspace behavior.
	summary, err = Ingest(ctx, st, sourcePath, cacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pages != 3 || summary.Blocks != 5 || summary.Comments != 3 {
		t.Fatalf("unexpected unscoped ingestion: %+v", summary)
	}

	// Narrowing coverage must not apply an excluded workspace's tombstones.
	if _, err := source.Exec(`update block set alive = 0; update comment set alive = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(ctx, st, sourcePath, cacheDir, scope); err != nil {
		t.Fatal(err)
	}
	for table, ids := range map[string][]string{"pages": {"page-a", "page-b", "page-unknown"}, "blocks": {"block-a", "block-b", "page-unknown"}, "comments": {"comment-a", "comment-b", "comment-unknown"}} {
		for i, id := range ids {
			want := i != 0
			var alive bool
			if err := st.DB().QueryRow("select alive from "+table+" where id = ?", id).Scan(&alive); err != nil || alive != want {
				t.Fatalf("%s/%s alive=%t want=%t err=%v", table, id, alive, want, err)
			}
		}
	}

	// Multiple IDs select a union; duplicate normalized IDs do not duplicate rows.
	summary, err = Ingest(ctx, st, sourcePath, cacheDir, []string{scope[0], "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
	if err != nil || summary.Pages != 2 || summary.Blocks != 4 || summary.Comments != 2 {
		t.Fatalf("unexpected union ingestion: %+v, %v", summary, err)
	}
}
