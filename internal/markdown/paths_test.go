package markdown

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestPathResolverResolvesPageTeamThroughCollectionParent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := store.NowMS()
	if err := st.UpsertTeam(ctx, store.Team{ID: "team1", SpaceID: "space1", Name: "Research", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCollection(ctx, store.Collection{ID: "collection1", SpaceID: "space1", ParentID: "team1", ParentTable: "team", Name: "Roadmap", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	page := store.Page{ID: "page1", SpaceID: "space1", ParentID: "collection1", ParentTable: "collection", CollectionID: "collection1", Title: "Row", Alive: true, Source: "test", SyncedAt: now}
	if err := st.UpsertPage(ctx, page); err != nil {
		t.Fatal(err)
	}

	paths, err := newPathResolver(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if teamID := paths.pageTeamID(page); teamID != "team1" {
		t.Fatalf("expected team1, got %q", teamID)
	}
}

func TestPathResolverResolvesPageTeamThroughBlockParent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := store.NowMS()
	if err := st.UpsertTeam(ctx, store.Team{ID: "team1", SpaceID: "space1", Name: "Research", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "block1", SpaceID: "space1", ParentID: "team1", ParentTable: "team", Type: "text", Text: "parent", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	page := store.Page{ID: "page1", SpaceID: "space1", ParentID: "block1", ParentTable: "block", Title: "Child", Alive: true, Source: "test", SyncedAt: now}
	if err := st.UpsertPage(ctx, page); err != nil {
		t.Fatal(err)
	}

	paths, err := newPathResolver(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if teamID := paths.pageTeamID(page); teamID != "team1" {
		t.Fatalf("expected team1, got %q", teamID)
	}
}
