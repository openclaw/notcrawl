package share

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/store"
)

func TestShareProjectsSignedFilesWithoutChangingArchive(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	signedURL := url.URL{Scheme: "https", Host: "files.example", Path: "/file"}
	signedURL.RawQuery = url.Values{"X-Amz-Signature": {"redacted"}, "X-Amz-Credential": {"placeholder"}, "download": {"1"}}.Encode()
	signed := signedURL.String()
	properties := `{"files":{"type":"files","files":[{"type":"file","file":{"url":"` + signed + `"}}]}}`
	raw := `{"properties":` + properties + `,"number":9007199254740993}`
	page := store.Page{ID: "page", Title: "Files", Alive: true, Source: store.SourceAPI, SyncedAt: 1, PropertiesJSON: properties, RawJSON: raw}
	if err := st.UpsertPage(ctx, page); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRawRecord(ctx, store.RawRecord{Source: store.SourceAPI, RecordTable: "page", RecordID: "page", RawJSON: raw, SyncedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBlock(ctx, store.Block{ID: "file", PageID: "page", ParentID: "page", Type: "file", Text: signed, Alive: true, Source: store.SourceAPI, SyncedAt: 1, PropertiesJSON: `{"file":{"url":"` + signed + `"}}`, RawJSON: raw}); err != nil {
		t.Fatal(err)
	}
	md := t.TempDir()
	exported, err := (markdown.Exporter{Store: st, Dir: md}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exported.Files[0])
	if err != nil || strings.Contains(string(body), "X-Amz-") {
		t.Fatalf("Markdown retained capability: %s %v", body, err)
	}
	repo := t.TempDir()
	result, err := Publish(ctx, st, PublishOptions{RepoPath: repo, MarkdownDir: md})
	if err != nil {
		t.Fatal(err)
	}
	tables := append([]TableManifest{}, result.Manifest.Tables...)
	if result.Manifest.RecordSources != nil {
		tables = append(tables, *result.Manifest.RecordSources)
	}
	for _, table := range tables {
		file, err := os.Open(filepath.Join(repo, table.Path))
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		payload, err := io.ReadAll(gz)
		gz.Close()
		file.Close()
		if err != nil || strings.Contains(string(payload), "X-Amz-") {
			t.Fatalf("table %s retained capability: %v", table.Name, err)
		}
		if table.Name == "pages" && !strings.Contains(string(payload), "9007199254740993") {
			t.Fatal("numeric raw field changed")
		}
	}
	dst, err := store.Open(filepath.Join(t.TempDir(), "destination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, dst, repo); err != nil {
		t.Fatal(err)
	}
	var localRaw, importedRaw string
	if err := st.DB().QueryRowContext(ctx, "select raw_json from pages where id = 'page'").Scan(&localRaw); err != nil {
		t.Fatal(err)
	}
	if err := dst.DB().QueryRowContext(ctx, "select raw_json from pages where id = 'page'").Scan(&importedRaw); err != nil {
		t.Fatal(err)
	}
	if localRaw != raw || strings.Contains(importedRaw, "X-Amz-") || !json.Valid([]byte(importedRaw)) {
		t.Fatal("projection mutated local data or broke import")
	}
}
