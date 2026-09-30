package notionapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestWalkBlocksIndexesSimpleTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blocks/page/children":
			_, _ = io.WriteString(w, `{"results":[{"object":"block","id":"table","type":"table","has_children":true,"table":{"table_width":2,"has_column_header":false}}],"has_more":false}`)
		case "/blocks/table/children":
			_, _ = io.WriteString(w, `{"results":[{"object":"block","id":"row","type":"table_row","has_children":false,"table_row":{"cells":[[{"plain_text":"moonstone"}],[{"plain_text":"opal"}]]}}],"has_more":false}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPage(ctx, store.Page{ID: "page", Title: "Table", Alive: true, Source: SourceName, SyncedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Client{BaseURL: server.URL, HTTP: server.Client()}).walkBlocks(ctx, st, "page", "page", ""); err != nil {
		t.Fatal(err)
	}
	blocks, err := st.PageBlocks(ctx, "page")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if block.Type == "table_row" && block.Text != "moonstone opal" {
			t.Errorf("table row text=%q", block.Text)
		}
	}
	results, err := st.Search(ctx, "moonstone", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("table search=%v error=%v", results, err)
	}
}
