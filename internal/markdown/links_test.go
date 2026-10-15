package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestExportArchivedLinkDestinations(t *testing.T) {
	for _, source := range []string{store.SourceAPI, store.SourceDesktop} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.UpsertPage(ctx, store.Page{ID: "page", Title: "Links", Source: source, Alive: true, SyncedAt: 1}); err != nil {
				t.Fatal(err)
			}
			properties := `{"external":{"url":"https://example.com/video?X-Amz-Signature=synthetic&download=1"}}`
			if source == store.SourceDesktop {
				properties = `{"source":[["https://example.com/video?X-Amz-Signature=synthetic&download=1"]]}`
			}
			if err := st.UpsertBlock(ctx, store.Block{ID: "video", PageID: "page", ParentID: "page", Type: "video", PropertiesJSON: properties, Source: source, Alive: true, SyncedAt: 1}); err != nil {
				t.Fatal(err)
			}
			summary, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(summary.Files[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "<https://example.com/video?download=1>") || strings.Contains(string(data), "synthetic") {
				t.Fatalf("missing sanitized link in export: %s", data)
			}
		})
	}
}

func TestRenderLinkedBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, kind, source, properties, text, want string
	}{
		{"API bookmark", "bookmark", store.SourceAPI, `{"url":"https://example.com/article"}`, "Read this", "Read this\n\n<https://example.com/article>\n\n"},
		{"API embed", "embed", store.SourceAPI, `{"url":"https://www.youtube.com/watch?v=example"}`, "", "<https://www.youtube.com/watch?v=example>\n\n"},
		{"API caption", "embed", store.SourceAPI, `{"url":"https://example.com","caption":[{"plain_text":"A caption"}]}`, "", "A caption\n\n<https://example.com>\n\n"},
		{"API video", "video", store.SourceAPI, `{"external":{"url":"https://www.youtube.com/watch?v=example"}}`, "", "<https://www.youtube.com/watch?v=example>\n\n"},
		{"API preview", "link_preview", store.SourceAPI, `{"url":"https://example.com/preview"}`, "", "<https://example.com/preview>\n\n"},
		{"Desktop bookmark", "bookmark", store.SourceDesktop, `{"source":[["https://example.com/article"]]}`, "Read this", "Read this\n\n<https://example.com/article>\n\n"},
		{"Desktop tweet", "tweet", store.SourceDesktop, `{"source":[["https://x.com/example/status/123"]]}`, "", "<https://x.com/example/status/123>\n\n"},
		{"Desktop video", "video", store.SourceDesktop, `{"source":[["https://www.youtube.com/watch?v=example"]]}`, "", "<https://www.youtube.com/watch?v=example>\n\n"},
		{"already URL text", "embed", store.SourceAPI, `{"url":"https://example.com"}`, "https://example.com", "<https://example.com>\n\n"},
		{"angle brackets", "embed", store.SourceAPI, `{"url":"https://example.com/a>b"}`, "", "<https://example.com/a%3Eb>\n\n"},
		{"fragment delimiter", "embed", store.SourceAPI, `{"url":"https://example.com/#a>b"}`, "", "<https://example.com/#a%3Eb>\n\n"},
		{"missing URL", "bookmark", store.SourceAPI, `{}`, "Read this", "Read this\n\n"},
		{"invalid properties", "embed", store.SourceAPI, `{`, "", "[embed]\n\n"},
		{"unsafe scheme", "embed", store.SourceAPI, `{"url":"javascript:alert(1)"}`, "", "[embed]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderBlock(&b, store.Block{Type: tc.kind, Source: tc.source, PropertiesJSON: tc.properties, Text: tc.text}, "")
			if got := b.String(); got != tc.want {
				t.Fatalf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}
