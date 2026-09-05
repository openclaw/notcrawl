package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/notionapi"
	"github.com/openclaw/notcrawl/internal/notionmcp"
	"github.com/openclaw/notcrawl/internal/store"
)

func TestSearchFieldCollapsesRecordSeparators(t *testing.T) {
	got := searchField("line one\nline\ttwo  line three")
	if got != "line one line two line three" {
		t.Fatalf("unexpected field: %q", got)
	}
}

func TestTUIJSONListsArchiveRowsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notcrawl.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	if err := st.UpsertCollection(ctx, store.Collection{ID: "db1", Name: "Roadmap", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID:             "page1",
		CollectionID:   "db1",
		Title:          "Launch Plan",
		URL:            "https://example.com/launch",
		Alive:          true,
		Source:         "test",
		SyncedAt:       now,
		LastEditedTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID:           "block1",
		PageID:       "page1",
		ParentID:     "page1",
		Type:         "bulleted_list",
		Text:         "sync launch checklist",
		DisplayOrder: 1,
		Alive:        true,
		Source:       "test",
		SyncedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{"--config", filepath.Join(dir, "missing.toml"), "--db", dbPath, "tui", "--json"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("tui --json failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, stdout.String())
	}
	if len(rows) == 0 || rows[0]["title"] != "Launch Plan" || rows[0]["source"] != "notion" || rows[0]["kind"] != "page" || rows[0]["container"] != "Roadmap" || !strings.Contains(fmt.Sprint(rows[0]["text"]), "sync launch checklist") || !strings.Contains(fmt.Sprint(rows[0]["detail"]), "sync launch checklist") {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("tui --json mutated the sqlite database")
	}
}

func TestTUIAllRowsIncludesDatabasesWhenPagesHitLimit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notcrawl.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	if err := st.UpsertCollection(ctx, store.Collection{ID: "db1", Name: "Roadmap", Source: "test", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Launch Plan", "Backlog"} {
		if err := st.UpsertPage(ctx, store.Page{ID: title, CollectionID: "db1", Title: title, Alive: true, Source: "test", SyncedAt: now, LastEditedTime: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{"--config", filepath.Join(dir, "missing.toml"), "--db", dbPath, "tui", "--json", "--limit", "1"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("tui --json failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, stdout.String())
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[fmt.Sprint(row["kind"])] = true
	}
	if !seen["page"] || !seen["database"] {
		t.Fatalf("all rows should include pages and databases despite page limit: %#v", rows)
	}
}

func TestCollectionTUIRowsResolveParentCollectionNames(t *testing.T) {
	rows := collectionTUIRows([]store.Collection{{
		ID:          "child-db",
		SpaceID:     "space1",
		ParentID:    "parent-db",
		ParentTable: "collection",
		Name:        "Child",
		Source:      "test",
	}}, 10, nil, map[string]string{"parent-db": "Parent Database"}, map[string]string{"space1": "Workspace"})
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].ParentID != "Parent Database" {
		t.Fatalf("parent label = %q", rows[0].ParentID)
	}
	if rows[0].Scope != "Workspace" {
		t.Fatalf("scope = %q", rows[0].Scope)
	}
	if !strings.Contains(rows[0].Detail, "Parent: Parent Database") {
		t.Fatalf("detail = %q", rows[0].Detail)
	}
}

func TestTUIRowsHideRawNotionParentIDs(t *testing.T) {
	rows := pageTUIRows([]store.Page{{
		ID:             "page1",
		SpaceID:        "space1",
		ParentID:       "space:00b8cbcf-c520-4790-999a-9c2940263721",
		ParentTable:    "space",
		CollectionID:   "",
		Title:          "Launch Plan",
		Alive:          true,
		Source:         "test",
		LastEditedTime: 1000,
	}}, 10, nil, nil, map[string]string{"space1": "Comet.com", "00b8cbcf-c520-4790-999a-9c2940263721": "Comet.com"}, nil, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].ParentID != "Workspace: Comet.com" {
		t.Fatalf("parent label = %q", rows[0].ParentID)
	}

	rows = pageTUIRows([]store.Page{{
		ID:          "page2",
		SpaceID:     "space1",
		ParentID:    "330b54b1-d7cc-4cd7-96bc-4d705b5f37bf",
		ParentTable: "block",
		Title:       "Nested",
		Alive:       true,
		Source:      "test",
	}}, 10, nil, nil, map[string]string{"space1": "Comet.com"}, nil, nil)
	if rows[0].ParentID != "Workspace: Comet.com" {
		t.Fatalf("workspace fallback parent = %q", rows[0].ParentID)
	}
}

func TestTUIRowsHideNoisyNotionBlockParentLabels(t *testing.T) {
	rows := pageTUIRows([]store.Page{{
		ID:          "page1",
		SpaceID:     "space1",
		ParentID:    "block1",
		ParentTable: "block",
		Title:       "Child",
		Alive:       true,
		Source:      "test",
	}}, 10, map[string]string{
		"block1": "ce 2fd71240-10a3-80a0-a65a-007aec07c0d9 00b8cbcf-c520-4790-999a-9c2940263721 Pods",
	}, nil, map[string]string{"space1": "Comet.com"}, nil, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].ParentID != "Workspace: Comet.com" {
		t.Fatalf("noisy parent label = %q", rows[0].ParentID)
	}
}

func TestTUIRowsResolveBlockParentToOwningPage(t *testing.T) {
	rows := pageTUIRows([]store.Page{{
		ID:          "page1",
		SpaceID:     "space1",
		ParentID:    "block-child",
		ParentTable: "block",
		Title:       "Nested",
		Alive:       true,
		Source:      "test",
	}}, 10, map[string]string{
		"parent-page": "Customer Folder",
	}, nil, map[string]string{"space1": "Comet.com"}, map[string]store.ParentRef{
		"block-child":  {ID: "block-parent", Table: "block"},
		"block-parent": {ID: "parent-page", Table: "page"},
	}, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].ParentID != "Customer Folder" {
		t.Fatalf("resolved parent label = %q", rows[0].ParentID)
	}
}

func TestBlockPreviewKeepsNotionPageShape(t *testing.T) {
	blocks := []store.Block{
		{Type: "heading_1", Text: "Launch Plan"},
		{Type: "bulleted_list", Text: "ship tui"},
		{Type: "to_do", Text: "verify local binary"},
		{Type: "numbered_list", Text: "open terminal"},
		{Type: "quote", Text: "keep it readable"},
		{Type: "code", Text: "notcrawl tui"},
	}
	got := blockPreview(blocks, tuiPagePreviewMax)
	for _, want := range []string{"# Launch Plan", "- ship tui", "- [ ] verify local binary", "1. open terminal", "> keep it readable", "    notcrawl tui"} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview missing %q:\n%s", want, got)
		}
	}
}

func TestBlockPreviewCleansLegacyNotionMarkers(t *testing.T) {
	got := blockPreview([]store.Block{
		{Type: "paragraph", Text: "Option A: b"},
		{Type: "paragraph", Text: "Marketing Customer Reference Rights a https://example.com/sheet"},
	}, tuiPagePreviewMax)
	if strings.Contains(got, " a https://") || strings.Contains(got, ": b") {
		t.Fatalf("preview leaked legacy markers:\n%s", got)
	}
	for _, want := range []string{"Option A:", "Marketing Customer Reference Rights <https://example.com/sheet>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview missing %q:\n%s", want, got)
		}
	}
}

func TestBlockPreviewCompactsRepeatedLinkedPages(t *testing.T) {
	got := blockPreview([]store.Block{{
		Type: "paragraph",
		Text: "linked page, linked page, linked page Add details",
	}}, tuiPagePreviewMax)
	if got != "linked pages Add details" {
		t.Fatalf("got %q", got)
	}
}

func TestPagePreviewIncludesComments(t *testing.T) {
	got := pagePreview(
		[]store.Block{{Type: "paragraph", Text: "status update"}},
		[]store.Comment{{Text: "looks good"}, {Text: "ship it"}},
		tuiPagePreviewMax,
	)
	for _, want := range []string{"status update", "## Comments", "- looks good", "- ship it"} {
		if !strings.Contains(got, want) {
			t.Fatalf("page preview missing %q:\n%s", want, got)
		}
	}
}

func TestHelpMentionsTUI(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "tui") {
		t.Fatalf("help missing tui command:\n%s", stdout.String())
	}
}

func TestHelpAfterGlobalFlagsHasNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--config", configPath, "--db", filepath.Join(dir, "notcrawl.db"), "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage of notcrawl:") || !strings.Contains(stdout.String(), "tui") {
		t.Fatalf("help missing usage:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("unexpected stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help should not write config, stat err=%v", err)
	}
}

func TestInitHelpDoesNotWriteConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--config", configPath, "init", "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage of init:") {
		t.Fatalf("init help missing usage:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("unexpected stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init --help should not write config, stat err=%v", err)
	}
}

func TestReadCommandHelpDoesNotLoadConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "missing.toml")
	for _, args := range [][]string{
		{"--config", configPath, "search", "--help"},
		{"--config", configPath, "sql", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), args, &stdout, &stderr)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(stdout.String(), "Usage of") {
			t.Fatalf("%v help missing usage:\n%s", args, stdout.String())
		}
		if stderr.String() != "" {
			t.Fatalf("%v unexpected stderr:\n%s", args, stderr.String())
		}
	}
}

func TestVersionFlagWorksWithOtherGlobalFlags(t *testing.T) {
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "missing.toml"), "--version"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != version {
		t.Fatalf("version = %q", got)
	}
}

func TestMetadataDoesNotMarkPlainTextCommandsAsJSON(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"metadata"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Commands map[string]struct {
			JSON bool `json:"json"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("invalid metadata JSON: %v\n%s", err, stdout.String())
	}
	for _, name := range []string{"sync", "tap", "publish", "subscribe", "update"} {
		if manifest.Commands[name].JSON {
			t.Fatalf("%s should not be advertised as JSON", name)
		}
	}
	for _, name := range []string{"status", "doctor", "tui-json"} {
		if !manifest.Commands[name].JSON {
			t.Fatalf("%s should be advertised as JSON", name)
		}
	}
}

func TestShareImportHelpDocumentsRetentionModes(t *testing.T) {
	for _, command := range []struct {
		name string
		run  func(*bytes.Buffer) error
	}{
		{name: "subscribe", run: func(stdout *bytes.Buffer) error {
			return runSubscribe(context.Background(), stdout, config.Config{}, []string{"--help"})
		}},
		{name: "update", run: func(stdout *bytes.Buffer) error {
			return runUpdate(context.Background(), stdout, config.Config{}, []string{"--help"})
		}},
	} {
		var stdout bytes.Buffer
		if err := command.run(&stdout); err != nil {
			t.Fatalf("%s help: %v", command.name, err)
		}
		for _, flag := range []string{"-restore", "-retain-revisions"} {
			if !strings.Contains(stdout.String(), flag) {
				t.Fatalf("%s help missing %s:\n%s", command.name, flag, stdout.String())
			}
		}
	}
}

func TestSyncEmitsProgressPercentToStderr(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"--config", filepath.Join(dir, "missing.toml"),
		"--db", filepath.Join(dir, "notcrawl.db"),
		"sync", "--source", "desktop",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("sync failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	logs := stderr.String()
	for _, want := range []string{`msg="sync progress"`, `state=finished`, `percent=100.0`, `completion=100.0%`, `phase=desktop`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing %q in progress logs:\n%s", want, logs)
		}
	}
}

func TestSyncNotionMCPRunsThroughConfiguredCodexConnector(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notcrawl.db")
	pageID := "11111111-1111-1111-1111-111111111111"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: pageID, Title: "Title only", Alive: true, Source: store.SourceDesktop, SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			writeMainRPCResult(w, request.ID, map[string]any{"protocolVersion": "2025-03-26"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusNoContent)
		case "tools/list":
			writeMainRPCResult(w, request.ID, map[string]any{"tools": []map[string]any{
				{"name": "notion_fetch", "_meta": map[string]any{"connector_id": "test-notion", "connector_name": "Notion"}},
				{"name": "notion_search", "_meta": map[string]any{"connector_id": "test-notion", "connector_name": "Notion"}},
			}})
		case "tools/call":
			payload, _ := json.Marshal(map[string]any{
				"metadata": map[string]any{"type": "page"},
				"title":    "Fetched page",
				"url":      "https://notion.so/11111111111111111111111111111111",
				"text":     "<page><properties></properties><content>Connector CLI body</content></page>",
			})
			writeMainRPCResult(w, request.ID, map[string]any{
				"isError": false,
				"content": []map[string]any{{"type": "text", "text": string(payload)}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"test-token","account_id":"test-account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	configBody := fmt.Sprintf(`db_path = %q
cache_dir = %q
markdown_dir = %q

[notion.desktop]
enabled = false
path = ""

[notion.api]
enabled = false

[notion.mcp]
enabled = true
base_url = %q
auth_path = %q
connector_id = "test-notion"
max_pages = 10
`, dbPath, filepath.Join(dir, "cache"), filepath.Join(dir, "pages"), server.URL, authPath)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	originalClientFactory := newNotionMCPClient
	newNotionMCPClient = func(cfg config.Config) notionmcp.Client {
		client := originalClientFactory(cfg)
		client.AllowUnsafeBaseURL = true
		return client
	}
	defer func() {
		newNotionMCPClient = originalClientFactory
	}()
	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{
		"--config", configPath,
		"sync", "--source", "notion-mcp", "--page", pageID, "--limit", "1",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("notion-mcp sync failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "notion-mcp: candidates=1 pages=1 empty=0 failed=0") {
		t.Fatalf("unexpected stdout:\n%s", stdout.String())
	}
	for _, want := range []string{`phase=notion-mcp`, `state=finished`, `percent=100.0`} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("missing %q in progress logs:\n%s", want, stderr.String())
		}
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blocks, err := st.PageBlocks(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Type != store.BlockTypeNotionMCPMarkdown || blocks[0].Text != "Connector CLI body" {
		t.Fatalf("unexpected connector blocks: %#v", blocks)
	}
}

func writeMainRPCResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func TestWriteAPIWarnings(t *testing.T) {
	var stderr bytes.Buffer
	writeAPIWarnings(&stderr, notionapi.Summary{
		Warnings: []string{"Notion API user listing is forbidden.", "   ", "Notion API discovery returned zero pages."},
	})
	got := stderr.String()
	for _, want := range []string{
		"warning: Notion API user listing is forbidden.\n",
		"warning: Notion API discovery returned zero pages.\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in warnings:\n%s", want, got)
		}
	}
	if strings.Contains(got, "warning:    ") {
		t.Fatalf("blank warning should be skipped:\n%s", got)
	}
}

func TestTUIHelpReturnsUsage(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"tui", "--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage of tui:") || !strings.Contains(stdout.String(), "-limit") || !strings.Contains(stdout.String(), "right-click") || !strings.Contains(stdout.String(), "#              jump") {
		t.Fatalf("tui help missing usage:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("unexpected stderr:\n%s", stderr.String())
	}
}

func TestExportDatabaseAllWritesFilesAndIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notcrawl.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	for _, collection := range []store.Collection{
		{ID: "db1", Name: "Roadmap", Source: "test", SyncedAt: now, SchemaJSON: `{"Name":{"type":"title"}}`},
		{ID: "db2", Name: "Launch 🚀 Plan ✅\tQ3\nReview", Source: "test", SyncedAt: now, SchemaJSON: `{"Task":{"type":"title"}}`},
	} {
		if err := st.UpsertCollection(ctx, collection); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID: "page1", CollectionID: "db1", Title: "Ship", URL: "https://example.com/ship", Alive: true, Source: "test", SyncedAt: now,
		PropertiesJSON: `{"Name":{"type":"title","title":[{"plain_text":"Ship"}]}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "csv")
	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{"--config", filepath.Join(dir, "missing.toml"), "--db", dbPath, "export-db", "--all", "--dir", outDir}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("export-db --all failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "exported 2 databases and 1 rows") {
		t.Fatalf("unexpected stdout: %s", got)
	}
	for _, name := range []string{"roadmap-db1.csv", "launch-plan-q3-review-db2.csv", "index.tsv"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	indexFile, err := os.Open(filepath.Join(outDir, "index.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexFile.Close()
	indexReader := csv.NewReader(indexFile)
	indexReader.Comma = '\t'
	records, err := indexReader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("expected header plus two database rows, got %#v", records)
	}
	if got := strings.Join(records[0], "\t"); got != "id\tname\tsource\trows\tcolumns\tfile" {
		t.Fatalf("unexpected index header: %#v", records[0])
	}
	rowsByID := map[string][]string{}
	for _, record := range records[1:] {
		rowsByID[record[0]] = record
	}
	if row := rowsByID["db1"]; row[1] != "Roadmap" || row[5] != "roadmap-db1.csv" {
		t.Fatalf("unexpected db1 index row: %#v", row)
	}
	if row := rowsByID["db2"]; row[1] != "Launch 🚀 Plan ✅\tQ3\nReview" || row[5] != "launch-plan-q3-review-db2.csv" {
		t.Fatalf("escaped collection name did not round-trip: %#v", row)
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

func TestExportDatabaseOutputReturnsCloseError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notcrawl.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	if err := st.UpsertCollection(ctx, store.Collection{
		ID: "db1", Name: "Roadmap", Source: "test", SyncedAt: now, SchemaJSON: `{"Name":{"type":"title"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID: "page1", CollectionID: "db1", Title: "Ship", URL: "https://example.com/ship", Alive: true, Source: "test", SyncedAt: now,
		PropertiesJSON: `{"Name":{"type":"title","title":[{"plain_text":"Ship"}]}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	want := errors.New("disk full")
	orig := createExportOutput
	t.Cleanup(func() { createExportOutput = orig })
	createExportOutput = func(name string) (io.WriteCloser, error) {
		f, err := os.Create(name)
		if err != nil {
			return nil, err
		}
		return failOnCloseWriter{WriteCloser: f, closeErr: want}, nil
	}

	outputPath := filepath.Join(dir, "roadmap.csv")
	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{"--config", filepath.Join(dir, "missing.toml"), "--db", dbPath, "export-db", "--database", "db1", "--output", outputPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected export-db --output close error")
	}
	if !errors.Is(err, want) && !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("export-db --output error = %v", err)
	}
}

func TestExportDatabaseInvalidFormatDoesNotTruncateOutput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notcrawl.db")
	outputPath := filepath.Join(dir, "existing.csv")
	original := []byte("existing export")
	if err := os.WriteFile(outputPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{"--config", filepath.Join(dir, "missing.toml"), "--db", dbPath, "export-db", "--database", "db1", "--format", "json", "--output", outputPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("invalid export truncated output: %q", string(got))
	}
}

func seedSQLArchive(t *testing.T) (dbPath, configPath string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "notcrawl.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID: "page1", Title: "Launch Plan", Alive: true, Source: "test", SyncedAt: now, LastEditedTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath, filepath.Join(dir, "missing.toml")
}

func archivePageCountAndUserVersion(t *testing.T, dbPath string) (pages, userVersion int) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.DB().QueryRow(`select count(*) from pages`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`pragma user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	return pages, userVersion
}

func TestSQLCommandRejectsMutatingQueries(t *testing.T) {
	ctx := context.Background()
	for _, query := range []string{
		"SELECT 1; DELETE FROM pages",
		"WITH x AS (SELECT 1) DELETE FROM pages",
		"PRAGMA user_version=42",
	} {
		t.Run(query, func(t *testing.T) {
			dbPath, configPath := seedSQLArchive(t)
			var stdout, stderr bytes.Buffer
			err := run(ctx, []string{"--config", configPath, "--db", dbPath, "sql", query}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("expected read-only rejection, got err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			pages, userVersion := archivePageCountAndUserVersion(t, dbPath)
			if pages != 1 {
				t.Fatalf("pages mutated: count=%d", pages)
			}
			if userVersion != 0 {
				t.Fatalf("user_version mutated: %d", userVersion)
			}
		})
	}
}

func TestSQLCommandAllowsReadQueries(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		query string
		want  string
	}{
		{query: "SELECT title FROM pages", want: "Launch Plan"},
		{query: "WITH x AS (SELECT title FROM pages) SELECT * FROM x", want: "Launch Plan"},
		{query: "PRAGMA user_version", want: "0"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			dbPath, configPath := seedSQLArchive(t)
			var stdout, stderr bytes.Buffer
			err := run(ctx, []string{"--config", configPath, "--db", dbPath, "sql", tc.query}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("sql %q failed: %v\nstderr:\n%s", tc.query, err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("sql %q stdout missing %q:\n%s", tc.query, tc.want, stdout.String())
			}
			pages, userVersion := archivePageCountAndUserVersion(t, dbPath)
			if pages != 1 || userVersion != 0 {
				t.Fatalf("read query mutated archive: pages=%d user_version=%d", pages, userVersion)
			}
		})
	}
}
