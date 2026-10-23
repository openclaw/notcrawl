package notionmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/notcrawl/internal/store"
)

const (
	crawlPageA       = "11111111-1111-1111-1111-111111111111"
	crawlPageB       = "22222222-2222-2222-2222-222222222222"
	crawlDatabaseC   = "33333333-3333-3333-3333-333333333333"
	crawlPageD       = "44444444-4444-4444-4444-444444444444"
	crawlPageE       = "55555555-5555-5555-5555-555555555555"
	crawlPageF       = "66666666-6666-6666-6666-666666666666"
	crawlPageG       = "77777777-7777-7777-7777-777777777777"
	crawlCollectionH = "88888888-8888-8888-8888-888888888888"
)

type crawlDocument struct {
	kind string
	text string
}

type crawlFixture struct {
	mu            sync.Mutex
	documents     map[string]crawlDocument
	sweep         []searchResult
	withSchema    bool
	failOnce      map[string]bool
	rateLimitOnce bool
	fetched       []string
	rateLimited   map[string]bool
	searches      []map[string]any
}

func newCrawlFixture() *crawlFixture {
	compact := func(id string) string { return strings.ReplaceAll(id, "-", "") }
	return &crawlFixture{
		withSchema:  true,
		failOnce:    map[string]bool{crawlPageG: true},
		rateLimited: map[string]bool{},
		documents: map[string]crawlDocument{
			crawlPageA:       {kind: "page", text: `<page><content>Page A links <page url="https://www.notion.so/` + compact(crawlPageB) + `">B</page> and <database url="collection://` + crawlDatabaseC + `">C</database></content></page>`},
			crawlPageB:       {kind: "page", text: `<page><content>Back to https://www.notion.so/` + compact(crawlPageA) + ` and on to https://notion.so/Team/D-Title-` + compact(crawlPageD) + `?v=99999999999999999999999999999999</content></page>`},
			crawlDatabaseC:   {kind: "database", text: `<database><content>Rows live in <page url="https://www.notion.so/` + compact(crawlPageE) + `">E</page></content></database>`},
			crawlPageD:       {kind: "page", text: `<page><content>Page D with an external link https://example.com/00000000-0000-0000-0000-000000000000</content></page>`},
			crawlPageE:       {kind: "page", text: `<page><content>Page E</content></page>`},
			crawlPageF:       {kind: "page", text: `<page><content>Recently edited F</content></page>`},
			crawlPageG:       {kind: "page", text: `<page><content>Page G</content></page>`},
			crawlCollectionH: {kind: "database", text: `<database><content>Archived collection</content></database>`},
		},
		sweep: []searchResult{{ID: crawlPageF, Title: "F", Type: "page", Timestamp: "2026-06-01T12:00:00Z"}},
	}
}

func (f *crawlFixture) serve(w http.ResponseWriter, r *http.Request) {
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
		search := map[string]any{"name": "notion_search", "_meta": map[string]any{"connector_id": testConnectorID, "connector_name": "Notion"}}
		if f.withSchema {
			search["inputSchema"] = map[string]any{"type": "object", "properties": map[string]any{
				"query":     map[string]any{"type": "string"},
				"sort":      map[string]any{"type": "string", "enum": []string{"relevance", "last_edited", "created"}},
				"page_size": map[string]any{"type": "integer", "maximum": 50},
				"filters": map[string]any{"type": "object", "properties": map[string]any{
					"created_date_range":     map[string]any{"type": "object", "properties": map[string]any{"start_date": map[string]any{"type": "string"}, "end_date": map[string]any{"type": "string"}}},
					"last_edited_date_range": map[string]any{"type": "object", "properties": map[string]any{"start_date": map[string]any{"type": "string"}, "end_date": map[string]any{"type": "string"}}},
				}},
			}}
		}
		writeRPCResult(w, request.ID, map[string]any{"tools": []map[string]any{
			{"name": "notion_fetch", "_meta": map[string]any{"connector_id": testConnectorID, "connector_name": "Notion"}},
			search,
		}})
	case "tools/call":
		name, _ := request.Params["name"].(string)
		arguments, _ := request.Params["arguments"].(map[string]any)
		switch name {
		case "notion_search":
			f.mu.Lock()
			f.searches = append(f.searches, arguments)
			results := append([]searchResult(nil), f.sweep...)
			f.mu.Unlock()
			payload, _ := json.Marshal(map[string]any{"results": results, "type": "workspace_search"})
			writeToolText(w, request.ID, string(payload))
		case "notion_fetch":
			id, _ := arguments["id"].(string)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.rateLimitOnce && !f.rateLimited[id] {
				f.rateLimited[id] = true
				w.Header().Set("Retry-After", "0")
				http.Error(w, "slow down", http.StatusTooManyRequests)
				return
			}
			f.fetched = append(f.fetched, id)
			if f.failOnce[id] {
				delete(f.failOnce, id)
				writeRPCResult(w, request.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "boom"}}})
				return
			}
			document, ok := f.documents[id]
			if !ok {
				writeRPCResult(w, request.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "not found"}}})
				return
			}
			payload, _ := json.Marshal(map[string]any{
				"metadata": map[string]any{"type": document.kind, "last_edited_time": "2026-05-01T00:00:00Z"},
				"title":    "Fetched " + id,
				"url":      "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
				"text":     document.text,
			})
			writeToolText(w, request.ID, string(payload))
		default:
			http.Error(w, "unknown tool", http.StatusBadRequest)
		}
	default:
		http.Error(w, "unknown method", http.StatusBadRequest)
	}
}

func (f *crawlFixture) fetchedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fetched...)
}

func (f *crawlFixture) resetFetched() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = nil
}

func newCrawlStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := store.NowMS()
	for _, page := range []store.Page{
		{ID: crawlPageA, Title: "A", LastEditedTime: 20, Alive: true, Source: store.SourceDesktop, SyncedAt: now},
		{ID: crawlPageG, Title: "G", LastEditedTime: 10, Alive: true, Source: store.SourceDesktop, SyncedAt: now},
	} {
		if err := st.UpsertPage(ctx, page); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertCollection(ctx, store.Collection{ID: crawlCollectionH, Name: "H", Source: store.SourceDesktop, SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	return st
}

func newCrawlClient(t *testing.T, fixture *crawlFixture) Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	t.Cleanup(server.Close)
	return Client{BaseURL: server.URL, AuthPath: writeAuthFile(t), ConnectorID: testConnectorID, AllowUnsafeBaseURL: true}
}

func TestDiscoverCrawlsSeedsLinksAndRecentEdits(t *testing.T) {
	ctx := context.Background()
	st := newCrawlStore(t)
	fixture := newCrawlFixture()
	client := newCrawlClient(t, fixture)

	first, err := client.Sync(ctx, st, SyncOptions{Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidates != 8 || first.Pages != 7 || first.Failed != 1 || first.EmptyPages != 0 {
		t.Fatalf("first run summary: %+v", first)
	}
	wantOrder := []string{crawlPageF, crawlPageA, crawlPageG, crawlCollectionH, crawlPageB, crawlDatabaseC, crawlPageD, crawlPageE}
	if got := fixture.fetchedIDs(); strings.Join(got, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("fetch order = %v, want %v", got, wantOrder)
	}
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]store.Page{}
	for _, page := range pages {
		stored[page.ID] = page
	}
	for _, id := range []string{crawlPageF, crawlPageA, crawlPageB, crawlDatabaseC, crawlPageD, crawlPageE, crawlCollectionH} {
		if stored[id].Source != store.SourceNotionMCP {
			t.Fatalf("record %s not persisted from connector: %+v", id, stored[id])
		}
		synced, err := st.HasSyncState(ctx, store.SourceNotionMCP, "page_content", id)
		if err != nil || !synced {
			t.Fatalf("record %s should be marked visited: synced=%v err=%v", id, synced, err)
		}
	}
	if _, ok := stored[crawlPageG]; ok && stored[crawlPageG].Source == store.SourceNotionMCP {
		t.Fatalf("failed page was persisted: %+v", stored[crawlPageG])
	}
	if stored[crawlPageA].LastEditedTime != parseTimestamp("2026-05-01T00:00:00Z") {
		t.Fatalf("metadata last edited was not applied: %+v", stored[crawlPageA])
	}
	lastRun, ok, err := st.SyncStateCursor(ctx, store.SourceNotionMCP, discoveryEntity, discoveryLastRunKey)
	if err != nil || !ok {
		t.Fatalf("last run cursor: ok=%v err=%v", ok, err)
	}
	if _, err := time.Parse(time.RFC3339, lastRun); err != nil {
		t.Fatalf("last run cursor %q: %v", lastRun, err)
	}
	fixture.mu.Lock()
	searches := append([]map[string]any(nil), fixture.searches...)
	fixture.mu.Unlock()
	if len(searches) != 1 || searches[0]["query"] != "" || searches[0]["page_size"] != float64(50) || searches[0]["sort"] != "last_edited" {
		t.Fatalf("sweep search arguments: %#v", searches)
	}
	filters, _ := searches[0]["filters"].(map[string]any)
	edited, _ := filters["last_edited_date_range"].(map[string]any)
	start, _ := edited["start_date"].(string)
	if _, err := time.Parse(time.DateOnly, start); err != nil {
		t.Fatalf("sweep filter start_date %q: %v", start, err)
	}

	fixture.resetFetched()
	second, err := client.Sync(ctx, st, SyncOptions{Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.Candidates != 1 || second.Pages != 1 || second.Failed != 0 {
		t.Fatalf("second run should only retry the failed page: %+v", second)
	}
	if got := fixture.fetchedIDs(); len(got) != 1 || got[0] != crawlPageG {
		t.Fatalf("second run fetched %v", got)
	}

	fixture.resetFetched()
	fixture.mu.Lock()
	fixture.sweep = []searchResult{
		{ID: crawlPageA, Title: "A", Type: "page", Timestamp: "2026-07-01T00:00:00Z"},
		{ID: crawlPageB, Title: "B", Type: "page", Timestamp: "2026-01-01T00:00:00Z"},
	}
	fixture.mu.Unlock()
	third, err := client.Sync(ctx, st, SyncOptions{Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if third.Candidates != 1 || third.Pages != 1 {
		t.Fatalf("third run should refetch only the newer edit: %+v", third)
	}
	if got := fixture.fetchedIDs(); len(got) != 1 || got[0] != crawlPageA {
		t.Fatalf("third run fetched %v", got)
	}
}

func TestDiscoverStopsAtLimit(t *testing.T) {
	ctx := context.Background()
	st := newCrawlStore(t)
	fixture := newCrawlFixture()
	client := newCrawlClient(t, fixture)
	summary, err := client.Sync(ctx, st, SyncOptions{Discover: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 2 || summary.Pages != 2 {
		t.Fatalf("limited summary: %+v", summary)
	}
	if got := fixture.fetchedIDs(); len(got) != 2 || got[0] != crawlPageF || got[1] != crawlPageA {
		t.Fatalf("limited fetches: %v", got)
	}
	if !strings.Contains(strings.Join(summary.Warnings, "\n"), "stopped at limit 2") {
		t.Fatalf("missing limit warning: %#v", summary.Warnings)
	}
	fixture.mu.Lock()
	sweepSize := fixture.searches[0]["page_size"]
	fixture.mu.Unlock()
	if sweepSize != float64(1) {
		t.Fatalf("sweep page size under limit 2 = %v, want 1", sweepSize)
	}
	fixture.resetFetched()
	resumed, err := client.Sync(ctx, st, SyncOptions{Discover: true, Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	wantResumed := []string{crawlPageG, crawlCollectionH, crawlPageB, crawlDatabaseC, crawlPageD, crawlPageE}
	if got := fixture.fetchedIDs(); resumed.Candidates != 6 || strings.Join(got, ",") != strings.Join(wantResumed, ",") {
		t.Fatalf("resume did not follow stored links: %+v %v", resumed, got)
	}
}

func TestDiscoverRetriesRateLimitedCalls(t *testing.T) {
	ctx := context.Background()
	st := newCrawlStore(t)
	fixture := newCrawlFixture()
	fixture.rateLimitOnce = true
	client := newCrawlClient(t, fixture)
	summary, err := client.Sync(ctx, st, SyncOptions{Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 8 || summary.Pages != 7 || summary.Failed != 1 {
		t.Fatalf("rate limited summary: %+v", summary)
	}
	fixture.mu.Lock()
	rateLimited := len(fixture.rateLimited)
	fixture.mu.Unlock()
	if rateLimited != 8 {
		t.Fatalf("rate limited responses = %d, want 8", rateLimited)
	}
}

func TestDiscoverWithoutDateFilterSkipsSweep(t *testing.T) {
	ctx := context.Background()
	st := newCrawlStore(t)
	fixture := newCrawlFixture()
	fixture.withSchema = false
	client := newCrawlClient(t, fixture)
	summary, err := client.Sync(ctx, st, SyncOptions{Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 7 || contains(fixture.fetchedIDs(), crawlPageF) {
		t.Fatalf("sweep ran without a date filter: %+v %v", summary, fixture.fetchedIDs())
	}
	if !strings.Contains(strings.Join(summary.Warnings, "\n"), "neither a last-edited sort nor a last-edited date filter") {
		t.Fatalf("missing schema warning: %#v", summary.Warnings)
	}
	fixture.mu.Lock()
	searchCount := len(fixture.searches)
	fixture.mu.Unlock()
	if searchCount != 0 {
		t.Fatalf("search calls = %d, want 0", searchCount)
	}
}

func TestResolveNotionToolsPrefersExactSearchAndFetchInEitherOrder(t *testing.T) {
	meta := &toolMeta{ConnectorID: testConnectorID, ConnectorName: "Notion"}
	schema := json.RawMessage(`{"properties":{"sort":{"enum":["last_edited"]}}}`)
	variants := []toolDefinition{
		{Name: "notion_notion-search-emails", Meta: meta},
		{Name: "notion_notion-memory-search", Meta: meta},
		{Name: "notion_notion-search-agents", Meta: meta},
		{Name: "notion_notion-get-file-download-urls", Title: "Fetch download URLs", Meta: meta},
	}
	exact := []toolDefinition{
		{Name: "notion_notion-search", Meta: meta, InputSchema: schema},
		{Name: "notion_notion-fetch", Meta: meta},
	}
	for name, order := range map[string][]toolDefinition{
		"variants first": append(append([]toolDefinition{}, variants...), exact...),
		"exact first":    append(append([]toolDefinition{}, exact...), variants...),
	} {
		tools, err := resolveNotionTools(order, testConnectorID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tools.Search != "notion_notion-search" || tools.Fetch != "notion_notion-fetch" || lastEditedSort(tools.SearchSchema) != "last_edited" {
			t.Fatalf("%s: resolved tools = %+v", name, tools)
		}
	}
	fallback, err := resolveNotionTools([]toolDefinition{
		{Name: "notion_notion-search-emails", Meta: meta},
		{Name: "notion_notion-get-file-download-urls", Title: "Fetch download URLs", Meta: meta},
	}, testConnectorID)
	if err != nil || fallback.Search != "notion_notion-search-emails" {
		t.Fatalf("substring fallback without exact tools: %+v %v", fallback, err)
	}
}

func TestRetryDelayHonoursRetryAfter(t *testing.T) {
	if got := retryDelay("3", 0); got != 3*time.Second {
		t.Fatalf("seconds delay = %v", got)
	}
	if got := retryDelay("", 2); got != 4*time.Second {
		t.Fatalf("fallback delay = %v", got)
	}
	if got := retryDelay("", 20); got != maxRetryDelay {
		t.Fatalf("capped delay = %v", got)
	}
	if got := retryDelay(time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), 0); got != 0 {
		t.Fatalf("past http date delay = %v", got)
	}
}

func TestLinkedIDsFollowsNotionReferencesOnly(t *testing.T) {
	result := fetchResult{Text: `<page url="https://www.notion.so/Title-` + strings.ReplaceAll(crawlPageB, "-", "") + `?v=` + strings.ReplaceAll(crawlPageD, "-", "") + `">B</page>
collection://` + crawlDatabaseC + ` https://example.com/` + crawlPageE + ` https://www.notion.so/` + strings.ReplaceAll(crawlPageA, "-", "")}
	got := linkedIDs(result, crawlPageA)
	if strings.Join(got, ",") != crawlPageB+","+crawlDatabaseC {
		t.Fatalf("linked IDs = %v", got)
	}
}
