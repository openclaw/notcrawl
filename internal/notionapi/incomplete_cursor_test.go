package notionapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestIncompleteCursorPreservesUnseenBlocks(t *testing.T) {
	for _, cursor := range []string{"missing", "null", `""`, "42"} {
		t.Run(cursor, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.UpsertPage(ctx, store.Page{ID: "page", Title: "test", Alive: true, Source: SourceName, SyncedAt: 1}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertBlock(ctx, store.Block{ID: "unseen", PageID: "page", ParentID: "page", Type: "paragraph", Text: "retained", Alive: true, Source: SourceName, SyncedAt: 1}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := obj{"results": []any{}, "has_more": true}
				if cursor != "missing" {
					var value any
					if err := json.Unmarshal([]byte(cursor), &value); err != nil {
						t.Error(err)
					}
					response["next_cursor"] = value
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := Client{BaseURL: server.URL, Token: "test-auth-token", HTTP: server.Client()}
			_, _, _, err = client.ingestPage(ctx, st, obj{"id": "page"}, ingestPageOptions{FetchBlocks: true})
			if err == nil || !strings.Contains(err.Error(), "next_cursor") {
				t.Fatalf("wanted invalid continuation, got %v", err)
			}
			var alive int
			if err := st.DB().QueryRowContext(ctx, "select alive from blocks where id = 'unseen'").Scan(&alive); err != nil || alive != 1 {
				t.Fatalf("unseen child retired: alive=%d err=%v", alive, err)
			}
			complete, err := st.HasSyncState(ctx, SourceName, "page_blocks", "page")
			if err != nil || complete {
				t.Fatalf("incomplete page marked complete: %v, %v", complete, err)
			}
		})
	}
}
