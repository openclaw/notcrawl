package notionapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/store"
)

type discoveryFixture struct {
	pages       []string
	rows        []string
	collections bool
	failure     string
}

func discoveryPage(id string) obj {
	return obj{"object": "page", "id": id, "properties": obj{
		"title": obj{"type": "title", "title": []any{obj{"plain_text": id + "title"}}},
	}}
}

func (f *discoveryFixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	response := obj{"results": []any{}, "has_more": false}
	operation := r.URL.Path
	if operation == "/search" {
		var body obj
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		operation = "search-" + body.mapObj("filter").string("value")
		if operation == "search-page" {
			for _, id := range f.pages {
				response["results"] = append(response["results"].([]any), discoveryPage(id))
			}
		} else if f.collections {
			response["results"] = []any{obj{"object": strings.TrimPrefix(operation, "search-"), "id": "collection"}}
		}
	} else if strings.HasSuffix(operation, "/query") {
		for _, id := range f.rows {
			response["results"] = append(response["results"].([]any), discoveryPage(id))
		}
	} else if strings.HasPrefix(operation, "/blocks/") {
		id := strings.Split(operation, "/")[2]
		response["results"] = []any{obj{"id": id + "block", "type": "paragraph", "paragraph": obj{
			"rich_text": []any{obj{"plain_text": id + "body"}},
		}}}
	} else if operation == "/comments" {
		id := r.URL.Query().Get("block_id")
		response["results"] = []any{obj{"id": id + "comment", "rich_text": []any{obj{"plain_text": id + "commentword"}}}}
	}
	if f.failure == operation {
		http.Error(w, "synthetic failure", http.StatusTeapot)
		return
	}
	if f.failure == "partial-blocks" && strings.HasPrefix(operation, "/blocks/") {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(obj{"code": "validation_error", "message": "Block type ai_block is not supported via the API for your bot type."})
		return
	}
	if f.failure == "pagination-"+operation {
		response["has_more"] = true // No cursor: this is not a complete enumeration.
	}
	for _, field := range []string{"has_more", "results", "id"} {
		if f.failure != "missing-"+field+"-"+operation {
			continue
		}
		if field == "id" {
			response["results"] = []any{obj{"object": "page"}}
		} else {
			delete(response, field)
		}
	}
	_ = json.NewEncoder(w).Encode(response)
}

func newDiscoveryTest(t *testing.T, f *discoveryFixture) (Client, *store.Store) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return Client{BaseURL: server.URL, Token: "synthetic-test-token", HTTP: server.Client()}, st
}

func assertDiscoveryPage(t *testing.T, st *store.Store, id string, alive bool) {
	t.Helper()
	ctx := context.Background()
	pages, err := st.Pages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, page := range pages {
		found = found || page.ID == id
	}
	if found != alive {
		t.Errorf("page %s visible = %v, want %v", id, found, alive)
	}
	for _, word := range []string{id + "title", id + "body", id + "commentword"} {
		results, err := st.Search(ctx, word, 10)
		if err != nil || (len(results) > 0) != alive {
			t.Errorf("search %s = %v, %v; want visible=%v", word, results, err, alive)
		}
	}
	for _, table := range []string{"page", "block", "comment"} {
		recordID := id
		if table != "page" {
			recordID += table
		}
		live, err := st.RecordHasLiveSource(ctx, table, recordID, SourceName)
		if err != nil || live != alive {
			t.Errorf("%s source alive = %v, %v; want %v", table, live, err, alive)
		}
	}
	complete, err := st.HasSyncState(ctx, SourceName, "page_blocks", id)
	if err != nil || complete != alive {
		t.Errorf("page block completion = %v, %v; want %v", complete, err, alive)
	}
}

func TestSyncRetiresOmittedPagesAndRestoresRediscoveredPages(t *testing.T) {
	f := &discoveryFixture{pages: []string{"first", "omitted"}}
	client, st := newDiscoveryTest(t, f)
	ctx := context.Background()
	if _, err := client.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	assertDiscoveryPage(t, st, "omitted", true)
	exporter := markdown.Exporter{Store: st, Dir: t.TempDir()}
	before, err := exporter.Export(ctx)
	if err != nil || before.Pages != 2 {
		t.Fatalf("initial export = %+v, %v", before, err)
	}
	f.pages = []string{"first"}
	if _, err := client.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	assertDiscoveryPage(t, st, "omitted", false)
	assertDiscoveryPage(t, st, "first", true)
	var reason, source string
	var deletedAt int64
	if err := st.DB().QueryRow(`select deletion_reason, deletion_source, deleted_at from record_sources
		where record_table = 'page' and record_id = 'omitted' and source = 'api'`).Scan(&reason, &source, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if reason != "complete-authoritative-enumeration" || source != SourceName || deletedAt <= 0 {
		t.Errorf("omission tombstone = %q %q %d", reason, source, deletedAt)
	}
	after, err := exporter.Export(ctx)
	if err != nil || after.Pages != 1 {
		t.Fatalf("retired export = %+v, %v", after, err)
	}
	for _, path := range before.Files {
		if path != after.Files[0] {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("stale Markdown retained: %v", err)
			}
		}
	}
	f.pages = []string{"first", "omitted"}
	if _, err := client.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	assertDiscoveryPage(t, st, "omitted", true)
	var tombstones int
	if err := st.DB().QueryRow(`select count(*) from record_sources where source = 'api'
		and (deleted_at is not null or deletion_source is not null or deletion_reason is not null)`).Scan(&tombstones); err != nil || tombstones != 0 {
		t.Errorf("revived source tombstones = %d, %v", tombstones, err)
	}
}

func TestSyncPreservesOmittedPagesWithoutCompleteDiscovery(t *testing.T) {
	for _, failure := range []string{
		"empty", "empty-collections", "search-page", "search-data_source", "pagination-search-page", "pagination-search-data_source",
		"/blocks/first/children", "pagination-/blocks/first/children", "partial-blocks", "/comments",
		"/data_sources/collection/query", "pagination-/data_sources/collection/query", "cancelled",
	} {
		t.Run(failure, func(t *testing.T) {
			f := &discoveryFixture{pages: []string{"first", "omitted"}}
			client, st := newDiscoveryTest(t, f)
			ctx := context.Background()
			if _, err := client.Sync(ctx, st); err != nil {
				t.Fatal(err)
			}
			f.pages, f.collections, f.failure = []string{"first"}, true, failure
			if failure == "empty" {
				f.pages, f.collections = nil, false
			}
			if failure == "empty-collections" {
				f.pages = nil
			}
			if failure == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			summary, err := client.Sync(ctx, st)
			if failure == "empty" || failure == "empty-collections" || failure == "partial-blocks" {
				if err != nil || len(summary.Warnings) == 0 {
					t.Fatalf("wanted incomplete discovery warning: %+v, %v", summary, err)
				}
			} else if err == nil {
				t.Fatal("wanted discovery failure")
			}
			assertDiscoveryPage(t, st, "omitted", true)
		})
	}
}

func TestSyncIncludesCollectionRowsInDiscovery(t *testing.T) {
	for _, version := range []string{"2022-06-28", "2026-03-11"} {
		t.Run(version, func(t *testing.T) {
			f := &discoveryFixture{pages: []string{"first", "row", "omitted"}}
			client, st := newDiscoveryTest(t, f)
			client.Version = version
			ctx := context.Background()
			if _, err := client.Sync(ctx, st); err != nil {
				t.Fatal(err)
			}
			f.pages, f.rows, f.collections = nil, []string{"row"}, true
			if _, err := client.Sync(ctx, st); err != nil {
				t.Fatal(err)
			}
			assertDiscoveryPage(t, st, "row", true)
			assertDiscoveryPage(t, st, "omitted", false)
		})
	}
}

func TestOmittedAPIPagePromotesOtherSources(t *testing.T) {
	for _, source := range []string{"desktop", "notion-mcp"} {
		t.Run(source, func(t *testing.T) {
			f := &discoveryFixture{pages: []string{"first", "omitted"}}
			client, st := newDiscoveryTest(t, f)
			ctx := context.Background()
			for _, id := range []string{"omitted", "otheronly"} {
				if err := st.UpsertPage(ctx, store.Page{ID: id, Title: "fallbacktitle", Alive: true, Source: source}); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertBlock(ctx, store.Block{ID: id + "block", PageID: id, ParentID: id, Type: "paragraph", Text: "fallbackbody", Alive: true, Source: source}); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertComment(ctx, store.Comment{ID: id + "comment", PageID: id, Text: "fallbackcomment", Alive: true, Source: source}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := client.Sync(ctx, st); err != nil {
				t.Fatal(err)
			}
			f.pages = []string{"first"}
			if _, err := client.Sync(ctx, st); err != nil {
				t.Fatal(err)
			}
			for _, word := range []string{"fallbacktitle", "fallbackbody", "fallbackcomment"} {
				results, err := st.Search(ctx, word, 10)
				if err != nil || len(results) != 2 {
					t.Errorf("fallback search %s = %v, %v", word, results, err)
				}
			}
			for _, word := range []string{"omittedtitle", "omittedbody", "omittedcommentword"} {
				results, err := st.Search(ctx, word, 10)
				if err != nil || len(results) != 0 {
					t.Errorf("retired API search %s = %v, %v", word, results, err)
				}
			}
		})
	}
}

func TestTargetedPageIngestionPreservesOtherPages(t *testing.T) {
	f := &discoveryFixture{pages: []string{"first", "omitted"}}
	client, st := newDiscoveryTest(t, f)
	ctx := context.Background()
	if _, err := client.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := client.ingestPage(ctx, st, discoveryPage("first"), ingestPageOptions{FetchBlocks: true, FetchComments: true}); err != nil {
		t.Fatal(err)
	}
	assertDiscoveryPage(t, st, "omitted", true)
}

func TestMalformedDiscoveryPreservesOmittedPages(t *testing.T) {
	for _, operation := range []string{"search-page", "search-data_source", "/data_sources/collection/query"} {
		for _, field := range []string{"has_more", "results", "id"} {
			t.Run(operation+"/"+field, func(t *testing.T) {
				f := &discoveryFixture{pages: []string{"first", "omitted"}}
				client, st := newDiscoveryTest(t, f)
				ctx := context.Background()
				if _, err := client.Sync(ctx, st); err != nil {
					t.Fatal(err)
				}
				f.pages, f.collections = []string{"first"}, true
				f.failure = "missing-" + field + "-" + operation
				if _, err := client.Sync(ctx, st); err == nil {
					t.Error("malformed discovery must fail")
				}
				assertDiscoveryPage(t, st, "omitted", true)
			})
		}
	}
}

func TestOmissionRetirementRollsBackWithSearchOnStorageFailure(t *testing.T) {
	f := &discoveryFixture{pages: []string{"first", "omitted"}}
	client, st := newDiscoveryTest(t, f)
	ctx := context.Background()
	if _, err := client.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`create trigger fail_retirement before update on record_sources
		when new.record_table = 'comment' and new.alive = 0
		begin select raise(abort, 'synthetic retirement failure'); end`); err != nil {
		t.Fatal(err)
	}
	f.pages = []string{"first"}
	if _, err := client.Sync(ctx, st); err == nil || !strings.Contains(err.Error(), "synthetic retirement failure") {
		t.Fatalf("wanted storage failure: %v", err)
	}
	assertDiscoveryPage(t, st, "omitted", true)
}
