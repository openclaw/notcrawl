package notionapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/store"
)

func TestSyncedCodeBlockWithBackticksExportsInsideFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/search" && strings.Contains(string(body), `"value":"page"`):
			_, _ = w.Write([]byte(`{"object":"list","has_more":false,"results":[
				{"object":"page","id":"page1","properties":{"title":{"type":"title","title":[{"plain_text":"Fence"}]}}}
			]}`))
		case r.URL.Path == "/blocks/page1/children":
			_, _ = w.Write([]byte(`{"object":"list","has_more":false,"results":[
				{"object":"block","id":"code1","type":"code","has_children":false,
				 "code":{"language":"markdown","rich_text":[{"type":"text","plain_text":"before\n` + "```" + `\nafter"}]}}
			]}`))
		default:
			_, _ = w.Write([]byte(`{"object":"list","has_more":false,"results":[]}`))
		}
	}))
	defer server.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "notcrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	client := Client{BaseURL: server.URL, Version: "2026-03-11", Token: "synthetic-test-token", HTTP: server.Client()}
	if _, err := client.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	exported, err := (markdown.Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Files) != 1 {
		t.Fatalf("exported files = %v", exported.Files)
	}
	data, err := os.ReadFile(exported.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	want := "````text\nbefore\n```\nafter\n````\n"
	if !strings.Contains(string(data), want) {
		t.Fatalf("code block fence broken; want %q in:\n%s", want, data)
	}
}
