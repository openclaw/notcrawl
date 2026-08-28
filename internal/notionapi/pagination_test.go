package notionapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestAPIPagination(t *testing.T) {
	for _, endpoint := range []struct {
		name, path, version string
		run                 func(context.Context, Client, *store.Store) (int, error)
	}{
		{"users", "/users", "2026-03-11", func(ctx context.Context, c Client, _ *store.Store) (int, error) {
			items, err := c.listUsers(ctx)
			return len(items), err
		}},
		{"pages", "/search", "2026-03-11", func(ctx context.Context, c Client, _ *store.Store) (int, error) {
			items, err := c.searchPages(ctx)
			return len(items), err
		}},
		{"collections", "/search", "2026-03-11", func(ctx context.Context, c Client, _ *store.Store) (int, error) {
			items, err := c.searchCollections(ctx)
			return len(items), err
		}},
		{"legacy-query", "/databases/database/query", "2022-06-28", func(ctx context.Context, c Client, st *store.Store) (int, error) {
			return c.queryCollection(ctx, st, "database")
		}},
		{"query", "/data_sources/database/query", "2026-03-11", func(ctx context.Context, c Client, st *store.Store) (int, error) {
			return c.queryCollection(ctx, st, "database")
		}},
		{"blocks", "/blocks/page/children", "2026-03-11", func(ctx context.Context, c Client, st *store.Store) (int, error) {
			count, _, err := c.walkBlocks(ctx, st, "page", "page", "space")
			return count, err
		}},
		{"comments", "/comments", "2026-03-11", func(ctx context.Context, c Client, st *store.Store) (int, error) {
			return c.ingestComments(ctx, st, "page", "space")
		}},
	} {
		for _, scenario := range []struct {
			name      string
			calls     int
			wantError bool
		}{
			{"long", 125, false},
			{"repeat", 2, true},
			{"cycle", 3, true},
			{"opaque", 2, false},
		} {
			t.Run(endpoint.name+"/"+scenario.name, func(t *testing.T) {
				var calls atomic.Int32
				next := ""
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					call := int(calls.Add(1))
					if r.URL.Path != endpoint.path || call > scenario.calls {
						http.Error(w, "unexpected pagination request", http.StatusInternalServerError)
						return
					}
					cursor := r.URL.Query().Get("start_cursor")
					if r.Method == http.MethodPost {
						var body obj
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						cursor = body.string("start_cursor")
					}
					if cursor != next {
						t.Errorf("cursor was altered: got %q, want %q", cursor, next)
					}
					next = fmt.Sprintf("cursor-%d", call)
					switch scenario.name {
					case "repeat":
						next = "private-cursor-marker"
					case "cycle":
						next = fmt.Sprintf("private-cursor-marker-%d", call%2)
					case "opaque":
						next = " opaque +/% cursor "
					}
					item := obj{"id": fmt.Sprintf("item-%d", call), "name": "Fixture", "type": "paragraph", "properties": obj{}, "paragraph": obj{"rich_text": []any{obj{"plain_text": "Fixture"}}}, "rich_text": []any{obj{"plain_text": "Fixture"}}}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(obj{"results": []any{item}, "has_more": scenario.wantError || call < scenario.calls, "next_cursor": next})
				}))
				defer server.Close()
				st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				count, err := endpoint.run(context.Background(), Client{BaseURL: server.URL, Version: endpoint.version, Token: "test-token", HTTP: server.Client()}, st)
				if scenario.wantError {
					if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
						t.Fatalf("error = %v, want repeated cursor", err)
					}
				} else if err != nil || count != scenario.calls {
					t.Fatalf("count = %d, error = %v; want %d items", count, err, scenario.calls)
				}
				if got := int(calls.Load()); got != scenario.calls {
					t.Fatalf("requests = %d, want %d", got, scenario.calls)
				}
			})
		}
	}
}

func TestBlockPaginationFailurePreservesArchive(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertBlock(ctx, store.Block{ID: "existing", PageID: "page", ParentID: "page", Type: "paragraph", Text: "Keep existing content", Source: SourceName, Alive: true, SyncedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncState(ctx, SourceName, "page_blocks", "page", "complete"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			fmt.Fprint(w, `{"results":[]}`)
		case "/search":
			fmt.Fprint(w, `{"results":[{"id":"page","properties":{}}]}`)
		case "/blocks/page/children":
			if calls.Add(1) > 2 {
				http.Error(w, "pagination did not stop", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"results":[{"id":"partial","type":"paragraph","paragraph":{"rich_text":[]}}],"has_more":true,"next_cursor":"private-cursor-marker"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	_, err = (Client{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}).Sync(ctx, st)
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") || calls.Load() != 2 {
		t.Fatalf("sync error = %v; requests = %d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "private-cursor-marker") {
		t.Fatal("pagination error exposes an opaque cursor")
	}
	var alive int
	if err := st.DB().QueryRowContext(ctx, `select alive from blocks where id = 'existing'`).Scan(&alive); err != nil || alive != 1 {
		t.Fatalf("existing block was lost: alive=%d error=%v", alive, err)
	}
	if synced, err := st.HasSyncState(ctx, SourceName, "page_blocks", "page"); err != nil || synced {
		t.Fatalf("partial page marked complete: synced=%v error=%v", synced, err)
	}
	if synced, err := st.HasSyncState(ctx, SourceName, "workspace", "default"); err != nil || synced {
		t.Fatalf("failed workspace marked complete: synced=%v error=%v", synced, err)
	}
}

func TestBlockPaginationCursorsAreScopedToEachParent(t *testing.T) {
	var total atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		parent := strings.Split(r.URL.Path, "/")[2]
		second := r.URL.Query().Get("start_cursor") != ""
		id := parent + "-first"
		if second {
			id = parent + "-second"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(obj{
			"results":  []any{obj{"id": id, "type": "paragraph", "has_children": parent == "page", "paragraph": obj{}}},
			"has_more": !second, "next_cursor": "same-cursor-for-every-parent",
		})
	}))
	defer server.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	count, _, err := (Client{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}).walkBlocks(context.Background(), st, "page", "page", "space")
	if err != nil || count != 6 || total.Load() != 6 {
		t.Fatalf("nested pagination: count=%d calls=%d error=%v", count, total.Load(), err)
	}
}
