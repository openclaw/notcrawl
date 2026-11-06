package report

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestBuildReport(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 4, 23, 5, 0, 0, 0, time.UTC)
	latest := now.Add(-time.Hour).UnixMilli()
	older := now.Add(-48 * time.Hour).UnixMilli()
	if err := st.UpsertSpace(ctx, store.Space{ID: "space1", Name: "HQ", Source: "test", SyncedAt: latest}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(ctx, store.User{ID: "user1", Name: "Ada", Source: "test", SyncedAt: latest}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCollection(ctx, store.Collection{ID: "db1", Name: "Roadmap", Source: "test", SyncedAt: latest}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", SpaceID: "space1", CollectionID: "db1", Title: "Launch", CreatedTime: older, LastEditedTime: latest, Alive: true, Source: "test", SyncedAt: latest}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page2", SpaceID: "space1", Title: "Notes", CreatedTime: older, LastEditedTime: older, Alive: true, Source: "test", SyncedAt: older}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "block1", PageID: "page1", Type: "paragraph", Text: "ship", CreatedTime: latest, Alive: true, Source: "test", SyncedAt: latest}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertComment(ctx, store.Comment{ID: "comment1", PageID: "page1", Text: "done", CreatedTime: latest, Alive: true, Source: "test", SyncedAt: latest}); err != nil {
		t.Fatal(err)
	}

	report, err := Build(ctx, st, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalSpaces != 1 || report.TotalUsers != 1 || report.TotalPages != 2 || report.TotalBlocks != 1 || report.TotalDatabases != 1 || report.TotalComments != 1 {
		t.Fatalf("unexpected totals: %+v", report)
	}
	if report.LatestEditedAt == nil || !report.LatestEditedAt.Equal(time.UnixMilli(latest).UTC()) {
		t.Fatalf("unexpected latest edit: %s", report.LatestEditedAt)
	}
	anchor := time.UnixMilli(latest).UTC()
	wantWindows := []WindowStats{
		{Label: "24 hours", Since: anchor.Add(-24 * time.Hour), EditedPages: 1, ActiveCollections: 1, Comments: 1},
		{Label: "7 days", Since: anchor.Add(-7 * 24 * time.Hour), EditedPages: 2, ActiveCollections: 1, Comments: 1},
		{Label: "30 days", Since: anchor.Add(-30 * 24 * time.Hour), EditedPages: 2, ActiveCollections: 1, Comments: 1},
	}
	if !reflect.DeepEqual(report.Windows, wantWindows) {
		t.Fatalf("windows = %+v, want %+v", report.Windows, wantWindows)
	}
	if len(report.TopCollections) == 0 || report.TopCollections[0].Name != "Roadmap" {
		t.Fatalf("unexpected top collections: %+v", report.TopCollections)
	}
	if len(report.TopSpaces) == 0 || report.TopSpaces[0].Name != "HQ" {
		t.Fatalf("unexpected top spaces: %+v", report.TopSpaces)
	}
	if len(report.RecentPages) != 2 || report.RecentPages[0].ID != "page1" {
		t.Fatalf("unexpected recent pages: %+v", report.RecentPages)
	}
}
