package notionapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestAPIFailureKeepsCommittedSearchConsistent(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "updated", true: "retired"}[retired], func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.UpsertPage(ctx, store.Page{ID: "first", Title: "obsoleteword", Alive: true, Source: SourceName, SyncedAt: 1}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/users":
					_ = json.NewEncoder(w).Encode(obj{"results": []any{}, "has_more": false})
				case "/search":
					_ = json.NewEncoder(w).Encode(obj{"results": []any{
						obj{"id": "first", "archived": retired, "properties": obj{"title": obj{"type": "title", "title": []any{obj{"plain_text": "currentword"}}}}},
						obj{"id": "second"},
					}, "has_more": false})
				case "/blocks/second/children":
					http.Error(w, "synthetic upstream failure", http.StatusTeapot)
				case "/blocks/first/children", "/comments":
					_ = json.NewEncoder(w).Encode(obj{"results": []any{}, "has_more": false})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.Error(w, "unexpected", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			_, err = (Client{BaseURL: server.URL, Token: "test", HTTP: server.Client()}).Sync(ctx, st)
			if err == nil || !strings.Contains(err.Error(), "418") {
				t.Fatalf("lost upstream error: %v", err)
			}
			old, err := st.Search(ctx, "obsoleteword", 10)
			if err != nil || len(old) != 0 {
				t.Fatalf("stale search after failure: %v %v", old, err)
			}
			current, err := st.Search(ctx, "currentword", 10)
			want := 1
			if retired {
				want = 0
			}
			if err != nil || len(current) != want {
				t.Fatalf("committed search: %v %v, want %d", current, err, want)
			}
			if err := st.DeferPageFTS(ctx, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			complete, err := st.HasSyncState(ctx, SourceName, "workspace", "default")
			if err != nil || complete {
				t.Fatalf("failed workspace marked complete: %v %v", complete, err)
			}
		})
	}
}

func TestAPIBatchRollsBackOnFTSFailureOrCancellation(t *testing.T) {
	for _, cancelBatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "fts failure", true: "cancellation"}[cancelBatch], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			page := store.Page{ID: "page", Title: "before", Alive: true, Source: SourceName, SyncedAt: 1}
			if err := st.UpsertPage(ctx, page); err != nil {
				t.Fatal(err)
			}
			if !cancelBatch {
				if _, err := st.DB().ExecContext(ctx, "drop table page_fts"); err != nil {
					t.Fatal(err)
				}
			}
			err = writePageBatch(ctx, st, func() error {
				page.Title, page.SyncedAt = "after", 2
				if err := st.UpsertPage(ctx, page); err != nil {
					return err
				}
				if cancelBatch {
					cancel()
				}
				return nil
			})
			if err == nil || (cancelBatch && !errors.Is(err, context.Canceled)) {
				t.Fatalf("expected batch error, got %v", err)
			}
			var title string
			if err := st.DB().QueryRowContext(context.Background(), "select title from pages where id = 'page'").Scan(&title); err != nil || title != "before" {
				t.Fatalf("canonical batch did not roll back: %q, %v", title, err)
			}
		})
	}
}
