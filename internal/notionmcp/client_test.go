package notionmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

const testConnectorID = "asdk_app_test_notion"

type gatewayFixture struct {
	mu          sync.Mutex
	fetched     []string
	searchQuery string
	initialized bool
}

func TestSyncRepairsIncompleteDesktopPages(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	pages := []store.Page{
		{ID: "11111111-1111-1111-1111-111111111111", Title: "Title only", Alive: true, Source: store.SourceDesktop, SyncedAt: now},
		{ID: "22222222-2222-2222-2222-222222222222", Title: "Complete", Alive: true, Source: store.SourceDesktop, SyncedAt: now},
		{ID: "33333333-3333-3333-3333-333333333333", Title: "Missing child", Alive: true, Source: store.SourceDesktop, SyncedAt: now},
		{ID: "44444444-4444-4444-4444-444444444444", Title: "API wins", Alive: true, Source: store.SourceDesktop, SyncedAt: now},
	}
	for _, page := range pages {
		if err := st.UpsertPage(ctx, page); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: "complete-body", PageID: pages[1].ID, ParentID: pages[1].ID, Type: "paragraph", Text: "already complete",
		Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: pages[2].ID, PageID: pages[2].ID, Type: "page", ContentJSON: `["missing-child"]`,
		Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID: pages[3].ID, Title: "API copy", Alive: true, Source: store.SourceAPI, SyncedAt: now + 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, store.SourceAPI, "page_blocks", pages[3].ID, "complete"); err != nil {
		t.Fatal(err)
	}

	fixture := &gatewayFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	authPath := writeAuthFile(t)
	summary, err := (Client{
		BaseURL:            server.URL,
		AuthPath:           authPath,
		ConnectorID:        testConnectorID,
		AllowUnsafeBaseURL: true,
	}).Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 2 || summary.Pages != 2 || summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	fixture.mu.Lock()
	fetched := append([]string(nil), fixture.fetched...)
	fixture.mu.Unlock()
	if len(fetched) != 2 || !contains(fetched, pages[0].ID) || !contains(fetched, pages[2].ID) {
		t.Fatalf("unexpected fetches: %#v", fetched)
	}
	for _, pageID := range []string{pages[0].ID, pages[2].ID} {
		blocks, err := st.PageBlocks(ctx, pageID)
		if err != nil {
			t.Fatal(err)
		}
		if len(blocks) == 0 || blocks[len(blocks)-1].Type != store.BlockTypeNotionMCPMarkdown {
			t.Fatalf("missing connector Markdown for %s: %#v", pageID, blocks)
		}
		text := blocks[len(blocks)-1].Text
		if !strings.Contains(text, "Connector body") {
			t.Fatalf("missing connector body: %q", text)
		}
		for _, secret := range []string{"X-Amz-Signature", "X-Amz-Credential", "secret-value"} {
			if strings.Contains(text, secret) {
				t.Fatalf("signed credential leaked into stored body: %q", text)
			}
		}
		synced, err := st.HasSyncState(ctx, store.SourceNotionMCP, "page_content", pageID)
		if err != nil || !synced {
			t.Fatalf("sync state for %s: synced=%v err=%v", pageID, synced, err)
		}
	}
	results, err := st.Search(ctx, "Connector", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected connector content in FTS, got %#v", results)
	}
	var raw string
	if err := st.DB().QueryRowContext(ctx, `select raw_json from raw_records where source = ? limit 1`, store.SourceNotionMCP).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "Connector body") || strings.Contains(raw, "secret-value") || strings.Contains(raw, "metadata-secret") {
		t.Fatalf("raw record stored connector content or credentials: %q", raw)
	}
	storedPages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range storedPages {
		if page.Source == store.SourceNotionMCP && strings.Contains(page.URL, "?") {
			t.Fatalf("signed connector URL was persisted: %q", page.URL)
		}
	}
}

func TestSyncReevaluatesRepairWhenDesktopSnapshotChanges(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pageID := "77777777-7777-7777-7777-777777777777"
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Title: "Partial", LastEditedTime: 10, Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: pageID, PageID: pageID, Type: "page", ContentJSON: `["missing"]`,
		Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fixture := &gatewayFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client := Client{
		BaseURL: server.URL, AuthPath: writeAuthFile(t), ConnectorID: testConnectorID, AllowUnsafeBaseURL: true,
	}
	first, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidates != 1 || first.Pages != 1 {
		t.Fatalf("initial sync: %+v", first)
	}
	unchanged, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Candidates != 0 || unchanged.Pages != 0 {
		t.Fatalf("unchanged snapshot was refetched: %+v", unchanged)
	}

	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Title: "Complete Desktop", LastEditedTime: 11, Alive: true, Source: store.SourceDesktop, SyncedAt: now + 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: "missing", PageID: pageID, ParentID: pageID, Type: "paragraph", Text: "new desktop body",
		Alive: true, Source: store.SourceDesktop, SyncedAt: now + 2,
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Candidates != 0 || completed.Pages != 0 {
		t.Fatalf("complete Desktop snapshot should retire the repair: %+v", completed)
	}
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Source != store.SourceDesktop || pages[0].Title != "Complete Desktop" {
		t.Fatalf("Desktop page was not restored: %#v", pages)
	}
	blocks, err := st.PageBlocks(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if block.Type == store.BlockTypeNotionMCPMarkdown {
			t.Fatalf("stale MCP block remained live: %#v", blocks)
		}
	}
	synced, err := st.HasSyncState(ctx, store.SourceNotionMCP, "page_content", pageID)
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("stale MCP state remained after Desktop became complete")
	}
	results, err := st.Search(ctx, "new desktop body", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != pageID {
		t.Fatalf("Desktop body was not reindexed: %#v", results)
	}
}

func TestSyncRefetchesRepairWhenPartialDesktopSnapshotChanges(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pageID := "88888888-8888-8888-8888-888888888888"
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Title: "Partial", LastEditedTime: 10, Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: pageID, PageID: pageID, Type: "page", ContentJSON: `["missing-one"]`,
		Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fixture := &gatewayFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client := Client{
		BaseURL: server.URL, AuthPath: writeAuthFile(t), ConnectorID: testConnectorID, AllowUnsafeBaseURL: true,
	}
	if _, err := client.Sync(ctx, st, SyncOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Title: "Still partial", LastEditedTime: 11, Alive: true, Source: store.SourceDesktop, SyncedAt: now + 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: pageID, PageID: pageID, Type: "page", ContentJSON: `["missing-two"]`,
		Alive: true, Source: store.SourceDesktop, SyncedAt: now + 2,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if second.Candidates != 1 || second.Pages != 1 {
		t.Fatalf("changed partial snapshot was not refetched: %+v", second)
	}
	fixture.mu.Lock()
	fetchCount := len(fixture.fetched)
	fixture.mu.Unlock()
	if fetchCount != 2 {
		t.Fatalf("fetch count = %d, want 2", fetchCount)
	}
}

func TestSyncRetiresRepairWhenDesktopPageIsTombstoned(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pageID := "99999999-9999-9999-9999-999999999999"
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Title: "Partial", Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fixture := &gatewayFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client := Client{
		BaseURL: server.URL, AuthPath: writeAuthFile(t), ConnectorID: testConnectorID, AllowUnsafeBaseURL: true,
	}
	if _, err := client.Sync(ctx, st, SyncOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Alive: false, Source: store.SourceDesktop, SyncedAt: now + 2,
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 0 || summary.Pages != 0 {
		t.Fatalf("tombstoned page should only retire its repair: %+v", summary)
	}
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 0 {
		t.Fatalf("tombstoned page remained live: %#v", pages)
	}
	blocks, err := st.PageBlocks(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("tombstoned page kept live repair blocks: %#v", blocks)
	}
}

func TestSyncRepairsIncompleteAPIBodyUntilAPISyncCompletes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pageID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{
		ID: pageID, Title: "Incomplete API page", Alive: true, Source: store.SourceAPI, SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fixture := &gatewayFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client := Client{
		BaseURL: server.URL, AuthPath: writeAuthFile(t), ConnectorID: testConnectorID, AllowUnsafeBaseURL: true,
	}
	repaired, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Candidates != 1 || repaired.Pages != 1 {
		t.Fatalf("incomplete API page was not repaired: %+v", repaired)
	}
	if err := st.SetSyncState(ctx, store.SourceAPI, "page_blocks", pageID, "complete"); err != nil {
		t.Fatal(err)
	}
	completed, err := client.Sync(ctx, st, SyncOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Candidates != 0 || completed.Pages != 0 {
		t.Fatalf("completed API page should only retire its repair: %+v", completed)
	}
	blocks, err := st.PageBlocks(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if block.Type == store.BlockTypeNotionMCPMarkdown {
			t.Fatalf("completed API page kept MCP repair: %#v", blocks)
		}
	}
	synced, err := st.HasSyncState(ctx, store.SourceNotionMCP, "page_content", pageID)
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("completed API page kept MCP sync state")
	}
}

func TestSyncTargetedQueryDiscoversPage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fixture := &gatewayFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	summary, err := (Client{
		BaseURL:            server.URL,
		AuthPath:           writeAuthFile(t),
		ConnectorID:        testConnectorID,
		AllowUnsafeBaseURL: true,
	}).Sync(ctx, st, SyncOptions{Queries: []string{"launch plan"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 1 || summary.Pages != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	fixture.mu.Lock()
	query := fixture.searchQuery
	fixture.mu.Unlock()
	if query != "launch plan" {
		t.Fatalf("query = %q", query)
	}
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].ID != "55555555-5555-5555-5555-555555555555" || pages[0].Source != store.SourceNotionMCP {
		t.Fatalf("unexpected pages: %#v", pages)
	}
	blocks, err := st.PageBlocks(ctx, pages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "## Properties") || !strings.Contains(blocks[0].Text, `"Status":"In progress"`) {
		t.Fatalf("connector properties were not preserved: %#v", blocks)
	}
}

func TestSyncLeavesExistingContentUntouchedWhenConnectorBodyIsEmpty(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pageID := "66666666-6666-6666-6666-666666666666"
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: pageID, Title: "Partial", Alive: true, Source: store.SourceDesktop, SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{
		ID: "desktop", PageID: pageID, ParentID: pageID, Type: "paragraph", Text: "keep this body",
		Alive: true, Source: store.SourceDesktop, SyncedAt: now,
	}); err != nil {
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
			writeRPCResult(w, request.ID, map[string]any{"protocolVersion": "2025-03-26"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusNoContent)
		case "tools/list":
			writeRPCResult(w, request.ID, map[string]any{"tools": []map[string]any{
				{"name": "notion_fetch", "_meta": map[string]any{"connector_id": testConnectorID, "connector_name": "Notion"}},
				{"name": "notion_search", "_meta": map[string]any{"connector_id": testConnectorID, "connector_name": "Notion"}},
			}})
		case "tools/call":
			payload, _ := json.Marshal(map[string]any{
				"title": "Partial",
				"text":  "<page><properties></properties><content>\n</content></page>",
			})
			writeToolText(w, request.ID, string(payload))
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	summary, err := (Client{
		BaseURL:            server.URL,
		AuthPath:           writeAuthFile(t),
		ConnectorID:        testConnectorID,
		AllowUnsafeBaseURL: true,
	}).Sync(ctx, st, SyncOptions{PageIDs: []string{pageID}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pages != 0 || summary.EmptyPages != 1 || len(summary.Warnings) != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	blocks, err := st.PageBlocks(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Text != "keep this body" || blocks[0].Source != store.SourceDesktop {
		t.Fatalf("existing blocks were replaced: %#v", blocks)
	}
	synced, err := st.HasSyncState(ctx, store.SourceNotionMCP, "page_content", pageID)
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("empty connector response was marked synced")
	}
}

func TestSanitizeSignedURLs(t *testing.T) {
	input := `[asset](https://files.example.com/a.png?X-Amz-Credential=abc&X-Amz-Signature=secret) ` +
		`oauth https://example.com/private?access_token=oauth-secret&id=kept ` +
		`normal https://example.com/?q=kept`
	got := sanitizeSignedURLs(input)
	if strings.Contains(got, "Credential") || strings.Contains(got, "Signature") ||
		strings.Contains(got, "access_token") || strings.Contains(got, "secret") {
		t.Fatalf("signed values remained: %s", got)
	}
	if !strings.Contains(got, "https://files.example.com/a.png)") ||
		!strings.Contains(got, "https://example.com/private") ||
		!strings.Contains(got, "https://example.com/?q=kept") {
		t.Fatalf("URLs were damaged: %s", got)
	}
}

func TestClientRejectsUntrustedBaseURLBeforeReadingAuth(t *testing.T) {
	_, _, err := (Client{
		BaseURL:  "http://attacker.example/apps",
		AuthPath: filepath.Join(t.TempDir(), "missing-auth.json"),
	}).gateway(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to send Codex credentials") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListAllToolsRejectsRepeatedCursor(t *testing.T) {
	pager := &repeatingCursorPager{cursor: "same"}
	server := httptest.NewServer(http.HandlerFunc(pager.serve))
	defer server.Close()

	_, err := (&gatewayClient{
		http:    server.Client(),
		baseURL: server.URL,
		auth:    authInfo{AccessToken: "test-token"},
	}).listAllTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), `repeated cursor "same"`) {
		t.Fatalf("err = %v, want repeated cursor", err)
	}
	if pager.calls != 2 {
		t.Fatalf("tools/list calls = %d, want 2", pager.calls)
	}
}

func TestListAllToolsRejectsUnboundedUniqueCursors(t *testing.T) {
	pager := &repeatingCursorPager{unique: true}
	server := httptest.NewServer(http.HandlerFunc(pager.serve))
	defer server.Close()

	_, err := (&gatewayClient{
		http:    server.Client(),
		baseURL: server.URL,
		auth:    authInfo{AccessToken: "test-token"},
	}).listAllTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeded 100 pages") {
		t.Fatalf("err = %v, want page limit", err)
	}
	if pager.calls != maxToolsListPages {
		t.Fatalf("tools/list calls = %d, want %d", pager.calls, maxToolsListPages)
	}
}

type repeatingCursorPager struct {
	cursor string
	unique bool
	calls  int
}

func (p *repeatingCursorPager) serve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.Method != "tools/list" {
		http.Error(w, "unexpected method "+request.Method, http.StatusBadRequest)
		return
	}
	p.calls++
	if !p.unique && p.calls > 5 {
		http.Error(w, "sticky pager was not stopped", http.StatusInternalServerError)
		return
	}
	cursor := p.cursor
	if p.unique {
		cursor = fmt.Sprintf("cursor-%d", p.calls)
	}
	writeRPCResult(w, request.ID, map[string]any{
		"tools":      []map[string]any{{"name": "notion_fetch"}},
		"nextCursor": cursor,
	})
}

func TestExtractEnhancedMarkdownPreservesProperties(t *testing.T) {
	got := extractEnhancedMarkdown(`<page><properties>{"Status":"In progress"}</properties><content>Body</content></page>`)
	for _, want := range []string{"## Properties", `    {"Status":"In progress"}`, "Body"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestPageIDFromReference(t *testing.T) {
	got := pageIDFromReference("https://notion.so/Page-ABCDEF0123456789ABCDEF0123456789")
	if got != "abcdef01-2345-6789-abcd-ef0123456789" {
		t.Fatalf("page ID = %q", got)
	}
}

func (f *gatewayFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("ChatGPT-Account-ID") != "test-account" {
		http.Error(w, "bad auth", http.StatusUnauthorized)
		return
	}
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
		writeRPCResult(w, request.ID, map[string]any{"protocolVersion": "2025-03-26"})
	case "notifications/initialized":
		f.mu.Lock()
		f.initialized = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		f.mu.Lock()
		initialized := f.initialized
		f.mu.Unlock()
		if !initialized {
			http.Error(w, "client did not complete initialization", http.StatusConflict)
			return
		}
		writeRPCResult(w, request.ID, map[string]any{"tools": []map[string]any{
			{"name": "notion_fetch", "title": "Notion_fetch", "_meta": map[string]any{"connector_id": testConnectorID, "connector_name": "Notion"}},
			{"name": "notion_search", "title": "Notion_search", "_meta": map[string]any{"connector_id": testConnectorID, "connector_name": "Notion"}},
			{"name": "legacy_fetch", "title": "Legacy fetch", "_meta": map[string]any{"connector_id": "legacy", "connector_name": "Notion (Legacy)"}},
		}})
	case "tools/call":
		name, _ := request.Params["name"].(string)
		arguments, _ := request.Params["arguments"].(map[string]any)
		switch name {
		case "notion_search":
			f.mu.Lock()
			f.searchQuery, _ = arguments["query"].(string)
			f.mu.Unlock()
			writeToolText(w, request.ID, `{"results":[{"id":"55555555-5555-5555-5555-555555555555","title":"Discovered","url":"https://notion.so/55555555555555555555555555555555","type":"page","timestamp":"2026-06-01T12:00:00Z"}],"type":"workspace_search"}`)
		case "notion_fetch":
			id, _ := arguments["id"].(string)
			f.mu.Lock()
			f.fetched = append(f.fetched, id)
			f.mu.Unlock()
			payload, _ := json.Marshal(map[string]any{
				"metadata": map[string]any{
					"type": "page",
					"assets": []any{
						map[string]any{"url": "https://files.example.com/icon.png?token=metadata-secret"},
					},
				},
				"title": "Fetched " + id,
				"url":   "https://notion.so/" + strings.ReplaceAll(id, "-", "") + "?token=secret-value",
				"text": `<page><properties>{"Status":"In progress"}</properties><content>## Connector body

[asset](https://files.example.com/file.png?X-Amz-Credential=value&X-Amz-Signature=secret-value)
</content></page>`,
			})
			writeToolText(w, request.ID, string(payload))
		default:
			http.Error(w, "unknown tool", http.StatusBadRequest)
		}
	default:
		http.Error(w, "unknown method", http.StatusBadRequest)
	}
}

func writeAuthFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"test-token","account_id":"test-account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRPCResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeToolText(w http.ResponseWriter, id any, text string) {
	writeRPCResult(w, id, map[string]any{
		"isError": false,
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
