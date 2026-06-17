package notionapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestSyncIngestsDatabasesAndRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
		case "/search":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			filter := body["filter"].(map[string]any)
			switch filter["value"] {
			case "page":
				_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
			case "database":
				_, _ = w.Write([]byte(`{
					"object":"list",
					"results":[{
						"object":"database",
						"id":"db1",
						"title":[{"type":"text","plain_text":"Roadmap","text":{"content":"Roadmap"}}],
						"parent":{"type":"workspace","workspace":true},
						"properties":{
							"Name":{"id":"title","type":"title","title":{}},
							"Status":{"id":"status","type":"select","select":{}}
						}
					}],
					"has_more":false
				}`))
			default:
				t.Fatalf("unexpected search filter: %v", filter["value"])
			}
		case "/databases/db1/query":
			_, _ = w.Write([]byte(`{
				"object":"list",
				"results":[{
					"object":"page",
					"id":"page1",
					"created_time":"2026-01-01T00:00:00Z",
					"last_edited_time":"2026-01-02T00:00:00Z",
					"archived":false,
					"in_trash":false,
					"url":"https://notion.so/page1",
					"parent":{"type":"database_id","database_id":"db1"},
					"properties":{
						"Name":{"id":"title","type":"title","title":[{"type":"text","plain_text":"Ship","text":{"content":"Ship"}}]},
						"Status":{"id":"status","type":"select","select":{"name":"Done"}}
					}
				}],
				"has_more":false
			}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	summary, err := (Client{BaseURL: server.URL, Version: "2022-06-28", Token: "secret"}).Sync(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Databases != 1 || summary.DatabaseRows != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	collections, err := st.Collections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(collections) != 1 || collections[0].ID != "db1" || collections[0].Name != "Roadmap" {
		t.Fatalf("unexpected collections: %+v", collections)
	}
	rows, err := st.CollectionPages(context.Background(), "db1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "page1" || rows[0].CollectionID != "db1" || rows[0].Title != "Ship" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestSyncIngestsCurrentDataSourcesAndRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
		case "/search":
			if got := r.Header.Get("Notion-Version"); got != "2026-03-11" {
				t.Fatalf("unexpected Notion-Version: %s", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			filter := body["filter"].(map[string]any)
			switch filter["value"] {
			case "page":
				_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
			case "data_source":
				_, _ = w.Write([]byte(`{
					"object":"list",
					"results":[{
						"object":"data_source",
						"id":"ds1",
						"title":[{"type":"text","plain_text":"Roadmap","text":{"content":"Roadmap"}}],
						"parent":{"type":"database_id","database_id":"db1"},
						"database_parent":{"type":"page_id","page_id":"page-parent"},
						"properties":{
							"Name":{"id":"title","type":"title","title":{}},
							"Status":{"id":"status","type":"select","select":{}}
						}
					}],
					"has_more":false
				}`))
			default:
				t.Fatalf("unexpected search filter: %v", filter["value"])
			}
		case "/data_sources/ds1/query":
			_, _ = w.Write([]byte(`{
				"object":"list",
				"results":[{
					"object":"page",
					"id":"page1",
					"created_time":"2026-01-01T00:00:00Z",
					"last_edited_time":"2026-01-02T00:00:00Z",
					"in_trash":false,
					"url":"https://notion.so/page1",
					"parent":{"type":"data_source_id","data_source_id":"ds1"},
					"properties":{
						"Name":{"id":"title","type":"title","title":[{"type":"text","plain_text":"Ship","text":{"content":"Ship"}}]},
						"Status":{"id":"status","type":"select","select":{"name":"Done"}}
					}
				}],
				"has_more":false
			}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	summary, err := (Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret"}).Sync(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Databases != 1 || summary.DatabaseRows != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	collections, err := st.Collections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(collections) != 1 || collections[0].ID != "ds1" || collections[0].ParentID != "db1" || collections[0].Name != "Roadmap" {
		t.Fatalf("unexpected collections: %+v", collections)
	}
	rows, err := st.CollectionPages(context.Background(), "ds1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "page1" || rows[0].CollectionID != "ds1" || rows[0].Title != "Ship" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestIngestCommentsSkipsRestrictedResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/comments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"object":"error","status":403,"code":"restricted_resource","message":"Insufficient permissions for this endpoint."}`))
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	count, err := (Client{BaseURL: server.URL, Version: "2022-06-28", Token: "secret", HTTP: http.DefaultClient}).ingestComments(context.Background(), st, "page1", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected comment count: %d", count)
	}
}

func TestSyncSkipsRestrictedResourceUsersAndContinuesDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"object":"error","status":403,"code":"restricted_resource","message":"Personal access tokens cannot list users."}`))
		case "/search":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			filter := body["filter"].(map[string]any)
			switch filter["value"] {
			case "page":
				_, _ = w.Write([]byte(`{
					"object":"list",
					"results":[{
						"object":"page",
						"id":"page1",
						"created_time":"2026-01-01T00:00:00Z",
						"last_edited_time":"2026-01-02T00:00:00Z",
						"archived":false,
						"in_trash":false,
						"url":"https://notion.so/page1",
						"parent":{"type":"workspace","workspace":true},
						"properties":{"Name":{"id":"title","type":"title","title":[{"type":"text","plain_text":"Shared page","text":{"content":"Shared page"}}]}}
					}],
					"has_more":false
				}`))
			case "database":
				_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
			default:
				t.Fatalf("unexpected search filter: %v", filter["value"])
			}
		case "/blocks/page1/children":
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
		case "/comments":
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	summary, err := (Client{BaseURL: server.URL, Version: "2022-06-28", Token: "secret"}).Sync(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Users != 0 || summary.Pages != 1 || len(summary.Warnings) != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Warnings[0] != "Notion API user listing is forbidden; continuing without user labels." {
		t.Fatalf("unexpected warning: %q", summary.Warnings[0])
	}
}

func TestSyncWarnsWhenAPIDiscoveryReturnsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users", "/search":
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPage(context.Background(), store.Page{ID: "local-page", Title: "Existing", Alive: true, Source: "desktop", SyncedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}

	summary, err := (Client{BaseURL: server.URL, Version: "2022-06-28", Token: "secret"}).Sync(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pages != 0 || summary.Databases != 0 || len(summary.Warnings) != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if want := "Notion API discovery returned zero pages, databases, blocks, and comments; check integration sharing and token scope. Existing local mirror still has 1 pages."; summary.Warnings[0] != want {
		t.Fatalf("unexpected warning:\nwant: %q\n got: %q", want, summary.Warnings[0])
	}
}

func TestIngestPageMarksBlockSyncOnlyAfterSuccessfulWalk(t *testing.T) {
	failBlocks := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/blocks/page1/children" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if failBlocks {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"object":"error","status":500,"code":"internal_server_error","message":"failed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
	}))
	defer server.Close()

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	page := obj{
		"id":       "page1",
		"archived": false,
		"in_trash": false,
		"parent":   map[string]any{"type": "workspace", "workspace": true},
		"properties": map[string]any{
			"Name": map[string]any{
				"id": "title", "type": "title",
				"title": []any{map[string]any{"plain_text": "Page"}},
			},
		},
	}
	client := Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret", HTTP: http.DefaultClient}
	if _, _, err := client.ingestPage(ctx, st, page, ingestPageOptions{FetchBlocks: true}); err != nil {
		t.Fatal(err)
	}
	synced, err := st.HasSyncState(ctx, SourceName, "page_blocks", "page1")
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("successful block walk did not mark page complete")
	}

	failBlocks = true
	if _, _, err := client.ingestPage(ctx, st, page, ingestPageOptions{FetchBlocks: true}); err == nil {
		t.Fatal("expected failed block walk")
	}
	synced, err = st.HasSyncState(ctx, SourceName, "page_blocks", "page1")
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("failed block walk retained completion marker")
	}
}

func TestIngestArchivedPageRestoresDesktopBlocksWithoutRefetching(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := store.NowMS()
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "Desktop page", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "block1", PageID: "page1", ParentID: "page1", Type: "text", Text: "Desktop body", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPage(ctx, store.Page{ID: "page1", Title: "API page", Alive: true, Source: SourceName, SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "block1", PageID: "page1", ParentID: "page1", Type: "paragraph", Text: "API body", Alive: true, Source: SourceName, SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertComment(ctx, store.Comment{ID: "comment1", PageID: "page1", Text: "Desktop comment", Alive: true, Source: "desktop", SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertComment(ctx, store.Comment{ID: "comment1", PageID: "page1", Text: "API comment", Alive: true, Source: SourceName, SyncedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	page := obj{
		"id":       "page1",
		"archived": true,
		"parent":   map[string]any{"type": "workspace", "workspace": true},
		"properties": map[string]any{
			"Name": map[string]any{
				"id":    "title",
				"type":  "title",
				"title": []any{map[string]any{"plain_text": "Archived API page"}},
			},
		},
	}
	if _, _, err := (Client{BaseURL: server.URL, Token: "secret"}).ingestPage(ctx, st, page, ingestPageOptions{FetchBlocks: true, FetchComments: true}); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("archived page made %d requests", requests)
	}
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "Desktop page" || pages[0].Source != "desktop" {
		t.Fatalf("restored page = %#v", pages)
	}
	blocks, err := st.PageBlocks(ctx, "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Text != "Desktop body" || blocks[0].Source != "desktop" {
		t.Fatalf("restored blocks = %#v", blocks)
	}
	comments, err := st.PageComments(ctx, "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Text != "Desktop comment" || comments[0].Source != "desktop" {
		t.Fatalf("Desktop comment was not restored: %#v", comments)
	}
}

func TestWalkBlocksSkipsSyncedBlockCopyChildren(t *testing.T) {
	requestedCopyChildren := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/blocks/page1/children":
			_, _ = w.Write([]byte(`{
				"object":"list",
				"results":[{
					"object":"block",
					"id":"copy1",
					"type":"synced_block",
					"has_children":true,
					"created_time":"2026-01-01T00:00:00Z",
					"last_edited_time":"2026-01-01T00:00:00Z",
					"synced_block":{"synced_from":{"type":"block_id","block_id":"source1"}}
				}],
				"has_more":false
			}`))
		case "/blocks/copy1/children":
			requestedCopyChildren = true
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"object":"error","status":404,"code":"object_not_found","message":"not found"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	count, err := (Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret", HTTP: http.DefaultClient}).walkBlocks(context.Background(), st, "page1", "page1", "space1")
	if err != nil {
		t.Fatal(err)
	}
	if requestedCopyChildren {
		t.Fatal("copied synced block children endpoint was requested")
	}
	if count != 1 {
		t.Fatalf("unexpected block count: %d", count)
	}
	blocks, err := st.PageBlocks(context.Background(), "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].ID != "copy1" || blocks[0].Type != "synced_block" {
		t.Fatalf("unexpected blocks: %+v", blocks)
	}
}

func TestWalkBlocksRetiresMissingAPIBlocksAfterSuccessfulWalk(t *testing.T) {
	includeBlock := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/blocks/page1/children" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if !includeBlock {
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"object":"list",
			"results":[{
				"object":"block",
				"id":"block1",
				"type":"paragraph",
				"has_children":false,
				"created_time":"2026-01-01T00:00:00Z",
				"last_edited_time":"2026-01-01T00:00:00Z",
				"paragraph":{"rich_text":[{"type":"text","plain_text":"remove me","text":{"content":"remove me"}}]}
			}],
			"has_more":false
		}`))
	}))
	defer server.Close()

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret", HTTP: http.DefaultClient}
	if _, err := client.walkBlocks(ctx, st, "page1", "page1", "space1"); err != nil {
		t.Fatal(err)
	}
	blocks, err := st.PageBlocks(ctx, "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected initial API block, got %#v", blocks)
	}

	includeBlock = false
	if _, err := client.walkBlocks(ctx, st, "page1", "page1", "space1"); err != nil {
		t.Fatal(err)
	}
	blocks, err = st.PageBlocks(ctx, "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("missing API block remained live: %#v", blocks)
	}
}

func TestWalkBlocksFetchesOriginalSyncedBlockChildren(t *testing.T) {
	requestedSourceChildren := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/blocks/page1/children":
			_, _ = w.Write([]byte(`{
				"object":"list",
				"results":[{
					"object":"block",
					"id":"source1",
					"type":"synced_block",
					"has_children":true,
					"created_time":"2026-01-01T00:00:00Z",
					"last_edited_time":"2026-01-01T00:00:00Z",
					"synced_block":{}
				}],
				"has_more":false
			}`))
		case "/blocks/source1/children":
			requestedSourceChildren = true
			_, _ = w.Write([]byte(`{
				"object":"list",
				"results":[{
					"object":"block",
					"id":"child1",
					"type":"paragraph",
					"has_children":false,
					"created_time":"2026-01-01T00:00:00Z",
					"last_edited_time":"2026-01-01T00:00:00Z",
					"paragraph":{"rich_text":[{"type":"text","plain_text":"Source child","text":{"content":"Source child"}}]}
				}],
				"has_more":false
			}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	count, err := (Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret", HTTP: http.DefaultClient}).walkBlocks(context.Background(), st, "page1", "page1", "space1")
	if err != nil {
		t.Fatal(err)
	}
	if !requestedSourceChildren {
		t.Fatal("original synced block children endpoint was not requested")
	}
	if count != 2 {
		t.Fatalf("unexpected block count: %d", count)
	}
	blocks, err := st.PageBlocks(context.Background(), "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].ID != "source1" || blocks[1].ID != "child1" {
		t.Fatalf("unexpected blocks: %+v", blocks)
	}
}

func TestIngestCommentsRetriesTransientGatewayError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/comments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"retryable":true,"retry_after":0}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"object":"list",
			"results":[{
				"id":"comment1",
				"rich_text":[{"type":"text","plain_text":"Looks good","text":{"content":"Looks good"}}],
				"created_by":{"id":"user1"},
				"created_time":"2026-01-01T00:00:00Z",
				"last_edited_time":"2026-01-01T00:00:00Z"
			}],
			"has_more":false
		}`))
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	count, err := (Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret", HTTP: http.DefaultClient}).ingestComments(context.Background(), st, "page1", "space1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || attempts != 2 {
		t.Fatalf("unexpected count/attempts: count=%d attempts=%d", count, attempts)
	}
	comments, err := st.PageComments(context.Background(), "page1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Text != "Looks good" {
		t.Fatalf("unexpected comments: %+v", comments)
	}
}

func TestIngestCommentsRetriesCloudflareTimeout(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path != "/comments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(524)
			_, _ = w.Write([]byte(`<!DOCTYPE html><title>Notion</title>`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"results":[],
			"has_more":false
		}`))
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	count, err := (Client{BaseURL: server.URL, Version: "2026-03-11", Token: "secret", HTTP: http.DefaultClient}).ingestComments(context.Background(), st, "page1", "space1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || attempts != 2 {
		t.Fatalf("unexpected count/attempts: count=%d attempts=%d", count, attempts)
	}
}
