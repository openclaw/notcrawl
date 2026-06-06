package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestExporterWritesMarkdown(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertSpace(ctx, store.Space{ID: "space1", Name: "Engineering", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", SpaceID: "space1", Title: "Launch Plan", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "block1", PageID: "page1", ParentID: "page1", Type: "bulleted_list", Text: "ship it", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Pages != 1 || len(s.Files) != 1 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "# Launch Plan") || !strings.Contains(text, "- ship it") {
		t.Fatalf("unexpected markdown:\n%s", text)
	}
}

func TestExporterUsesDisplayOrder(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Recipe", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, block := range []store.Block{
		{ID: "salt", PageID: "page1", ParentID: "page1", Type: "bulleted_list", Text: "salt", DisplayOrder: 2, CreatedTime: now, Alive: true, Source: "test", SyncedAt: now},
		{ID: "flour", PageID: "page1", ParentID: "page1", Type: "bulleted_list", Text: "flour", DisplayOrder: 1, CreatedTime: now, Alive: true, Source: "test", SyncedAt: now},
	} {
		if err := st.UpsertBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Index(text, "- flour") > strings.Index(text, "- salt") {
		t.Fatalf("markdown did not preserve display order:\n%s", text)
	}
}

func TestExporterMarksMissingDesktopBlocks(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Partial", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, block := range []store.Block{
		{ID: "page1", PageID: "page1", Type: "page", Text: "Partial", ContentJSON: `["cached","missing","moved"]`, Alive: true, Source: "desktop", SyncedAt: now},
		{ID: "cached", PageID: "page1", ParentID: "page1", Type: "text", Text: "Available body", Alive: true, Source: "desktop", SyncedAt: now},
		{ID: "moved", PageID: "other-page", ParentID: "other-page", Type: "text", Text: "Moved body", Alive: true, Source: "desktop", SyncedAt: now},
		{ID: "detached", PageID: "page1", ParentID: "old-parent", Type: "text", ContentJSON: `["detached-missing"]`, Alive: true, Source: "desktop", SyncedAt: now},
	} {
		if err := st.UpsertBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.IncompletePages != 1 || s.MissingBlockReferences != 2 {
		t.Fatalf("unexpected incomplete summary: %+v", s)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		"content_complete: false",
		"missing_block_references: 2",
		"Incomplete Desktop cache snapshot: 2 referenced blocks were not available locally.",
		"Available body",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q from markdown:\n%s", want, text)
		}
	}
}

func TestExporterDoesNotMarkCompleteCachedBlocksIncomplete(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Complete", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, block := range []store.Block{
		{ID: "page1", PageID: "page1", Type: "page", Text: "Complete", ContentJSON: `["child","subpage"]`, Alive: true, Source: "desktop", SyncedAt: now},
		{ID: "child", PageID: "page1", ParentID: "page1", Type: "text", Text: "Full body", Alive: true, Source: "desktop", SyncedAt: now},
		{ID: "subpage", PageID: "subpage", ParentID: "page1", Type: "page", Text: "Nested page", Alive: true, Source: "desktop", SyncedAt: now},
	} {
		if err := st.UpsertBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.IncompletePages != 0 || s.MissingBlockReferences != 0 {
		t.Fatalf("unexpected incomplete summary: %+v", s)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if text := string(b); strings.Contains(text, "content_complete") || strings.Contains(text, "[!WARNING]") {
		t.Fatalf("complete page marked incomplete:\n%s", text)
	}
}

func TestExporterRendersDatabaseProperties(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertCollection(ctx, store.Collection{
		ID:         "people",
		Name:       "People",
		SchemaJSON: `{"title":{"name":"Name","type":"title"},"people:email":{"name":"Email","type":"email"},"people:membership_type":{"name":"Membership Type","type":"select"},"people:job_title":{"name":"Title","type":"text"},"people:person":{"name":"Person","type":"person"},"owner_upper":{"name":"Owner","type":"text"},"owner_lower":{"name":"owner","type":"text"},"unsafe":{"name":"Risk ** [label]\n# heading","type":"text"}}`,
		Source:     "desktop",
		SyncedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(ctx, store.User{ID: "user1", Name: "Ada Lovelace", Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID:             "page1",
		ParentID:       "people",
		ParentTable:    "collection",
		Title:          "Ada",
		PropertiesJSON: `{"title":[["Ada"]],"people:email":[["ada@example.com"]],"people:membership_type":[["Member"]],"people:job_title":[["Engineer"]],"people:person":[["‣",[["u","user1"]]]],"owner_upper":[["Upper"]],"owner_lower":[["Lower"]],"unsafe":[["Safe"]]}`,
		Alive:          true,
		Source:         "desktop",
		SyncedAt:       now,
	}); err != nil {
		t.Fatal(err)
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"## Properties", "- **Email:** ada@example.com", "- **Membership Type:** Member", "- **Owner:** Upper", "- **owner:** Lower", "- **Person:** Ada Lovelace", "- **Risk \\*\\* \\[label\\] # heading:** Safe", "- **Title:** Engineer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q from markdown:\n%s", want, text)
		}
	}
	if strings.Contains(text, "- **Name:**") {
		t.Fatalf("title property should not be repeated:\n%s", text)
	}
	if strings.Index(text, "- **Owner:** Upper") > strings.Index(text, "- **owner:** Lower") {
		t.Fatalf("case-colliding properties were not deterministic:\n%s", text)
	}
}

func TestExporterPreservesSchemaLessProperties(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID:             "page1",
		Title:          "Ship",
		PropertiesJSON: `{"Task":[["Ship"]],"Name":[["Owner alias"]],"Title":[["Engineer"]]}`,
		Alive:          true,
		Source:         "desktop",
		SyncedAt:       now,
	}); err != nil {
		t.Fatal(err)
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "- **Name:** Owner alias") {
		t.Fatalf("schema-less Name property was dropped:\n%s", text)
	}
	if !strings.Contains(text, "- **Task:** Ship") {
		t.Fatalf("ambiguous schema-less Task property was dropped:\n%s", text)
	}
	if !strings.Contains(text, "- **Title:** Engineer") {
		t.Fatalf("schema-less Title property was dropped:\n%s", text)
	}
}

func TestExporterPreservesAmbiguousSchemaLessTitleMatches(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID:             "page1",
		Title:          "Ship",
		PropertiesJSON: `{"Task":[["Ship"]],"Status":[["Ship"]]}`,
		Alive:          true,
		Source:         "desktop",
		SyncedAt:       now,
	}); err != nil {
		t.Fatal(err)
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"- **Status:** Ship", "- **Task:** Ship"} {
		if !strings.Contains(text, want) {
			t.Fatalf("ambiguous schema-less property %q was dropped:\n%s", want, text)
		}
	}
}

func TestExporterExplainsEmptyDesktopPage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Empty or uncached", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "page1", PageID: "page1", Type: "page", ContentJSON: `["empty-column"]`, Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "empty-column", PageID: "page1", ParentID: "page1", Type: "column", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if text := string(b); !strings.Contains(text, "The page may be empty or its body may not have been cached.") {
		t.Fatalf("empty Desktop page lacked explanatory note:\n%s", text)
	}
}

func TestExporterPreservesAPIPropertyText(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID:             "page1",
		Title:          "API page",
		PropertiesJSON: `{"Description":{"type":"rich_text","rich_text":[{"type":"text","plain_text":"a https://example.com","text":{"content":"a https://example.com"}}]},"Website":{"type":"url","url":"first line\n# second line"}}`,
		Alive:          true,
		Source:         "api",
		SyncedAt:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID:          "page1",
		PageID:      "page1",
		Type:        "page",
		ContentJSON: `["stale-missing-child"]`,
		Alive:       true,
		Source:      "desktop",
		SyncedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, "api", "page_blocks", "page1", "complete"); err != nil {
		t.Fatal(err)
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "- **Description:** a https://example.com") {
		t.Fatalf("API property text was altered:\n%s", text)
	}
	if !strings.Contains(text, "- **Website:** first line<br># second line") {
		t.Fatalf("multiline API property was not kept inline:\n%s", text)
	}
	if strings.Contains(text, "content_complete: false") || strings.Contains(text, "Incomplete Desktop cache snapshot") {
		t.Fatalf("API-refreshed page used stale Desktop coverage:\n%s", text)
	}
}

func TestExporterRequiresCompletedAPIBlockSync(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Partial API page", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Partial API page", Alive: true, Source: "api", SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: "page1", PageID: "page1", Type: "page", ContentJSON: `["missing-child"]`,
		Alive: true, Source: "desktop", SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	s, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.IncompletePages != 1 || s.MissingBlockReferences != 1 {
		t.Fatalf("unfinished API block sync suppressed coverage: %+v", s)
	}
}

func TestExporterRemovesEmojiFromPathNames(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertSpace(ctx, store.Space{ID: "space1", Name: "研究 🚀", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", SpaceID: "space1", Title: "計画 ✅ / Q2", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "研究", "計画-q2-page1.md")
	if len(s.Files) != 1 || s.Files[0] != want {
		t.Fatalf("unexpected export path: %+v, want %s", s.Files, want)
	}
}

func TestExporterTruncatesMultibytePathNamesOnRuneBoundary(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	title := "где-у-меня-записаны-соображения-по-поводу-аллокации-затрат-на-подрядчиков-и-других-расходов"
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: title, Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 1 {
		t.Fatalf("unexpected export paths: %+v", s.Files)
	}
	name := filepath.Base(s.Files[0])
	if !utf8.ValidString(name) {
		t.Fatalf("export path is not valid UTF-8: %q", name)
	}
	if _, err := os.Stat(s.Files[0]); err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
}

func TestExporterUsesWorkspaceAndTeamspacePath(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertSpace(ctx, store.Space{ID: "space1", Name: "Acme Org", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTeam(ctx, store.Team{ID: "team1", SpaceID: "space1", Name: "Research Lab", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", SpaceID: "space1", ParentID: "team1", ParentTable: "team", Title: "Plan", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "acme-org", "research-lab", "plan-page1.md")
	if len(s.Files) != 1 || s.Files[0] != want {
		t.Fatalf("unexpected export path: %+v, want %s", s.Files, want)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `team_id: "team1"`) || !strings.Contains(text, `team: "Research Lab"`) {
		t.Fatalf("missing team front matter:\n%s", text)
	}
}

func TestExporterResolvesTeamspaceThroughCollectionParent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertSpace(ctx, store.Space{ID: "space1", Name: "Acme Org", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTeam(ctx, store.Team{ID: "team1", SpaceID: "space1", Name: "Research Lab", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCollection(ctx, store.Collection{ID: "collection1", SpaceID: "space1", ParentID: "team1", ParentTable: "team", Name: "Roadmap", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", SpaceID: "space1", ParentID: "collection1", ParentTable: "collection", CollectionID: "collection1", Title: "Row", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "acme-org", "research-lab", "row-page1.md")
	if len(s.Files) != 1 || s.Files[0] != want {
		t.Fatalf("unexpected export path: %+v, want %s", s.Files, want)
	}
}

func TestExporterUsesReadableMissingSpaceFallback(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	spaceID := "52f1c029-ec85-4ff5-bd43-c6d6ea9259e0"
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", SpaceID: spaceID, Title: "Loose", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s, err := Exporter{Store: st, Dir: dir}.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "space-52f1c029-ea9259e0", "loose-page1.md")
	if len(s.Files) != 1 || s.Files[0] != want {
		t.Fatalf("unexpected export path: %+v, want %s", s.Files, want)
	}
}

func TestExporterPrunesStaleMarkdown(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Launch", Alive: true, Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	staleDir := filepath.Join(dir, "old")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleMarkdown := filepath.Join(staleDir, "stale.md")
	if err := os.WriteFile(staleMarkdown, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	keepNote := filepath.Join(staleDir, "note.txt")
	if err := os.WriteFile(keepNote, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := (Exporter{Store: st, Dir: dir}).Export(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleMarkdown); !os.IsNotExist(err) {
		t.Fatalf("expected stale markdown to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(keepNote); err != nil {
		t.Fatalf("expected non-markdown file to remain: %v", err)
	}
}
