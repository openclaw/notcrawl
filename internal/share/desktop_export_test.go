package share

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/notiondesktop"
	"github.com/openclaw/notcrawl/internal/store"
)

func TestShareDesktopEnvelopesWithoutChangingArchive(t *testing.T) {
	for _, mode := range []string{"fresh", "existing archive", "Desktop fallback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			source := desktopSignedExportFixture(t)
			archive := filepath.Join(t.TempDir(), "archive.db")
			st, err := store.Open(archive)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			}()
			summary, err := notiondesktop.Ingest(ctx, st, source, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Pages != 1 || summary.Blocks != 2 || summary.RawRecords != 2 ||
				summary.Collections != 1 || summary.Comments != 1 {
				t.Fatalf("incomplete ingestion fixture: %+v", summary)
			}
			if mode == "existing archive" {
				// Export already-persisted Desktop envelopes without reingestion.
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				st, err = store.Open(archive)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "Desktop fallback" {
				if err := st.UpsertPage(ctx, store.Page{
					ID: "page", SpaceID: "space", Title: "API page", Source: store.SourceAPI,
					Alive: true, PropertiesJSON: `{}`, RawJSON: `{}`, SyncedAt: store.NowMS(),
				}); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertBlock(ctx, store.Block{
					ID: "file", PageID: "page", SpaceID: "space", ParentID: "page",
					Type: "paragraph", Text: "API body", Source: store.SourceAPI,
					Alive: true, PropertiesJSON: `{}`, RawJSON: `{}`, SyncedAt: store.NowMS(),
				}); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertComment(ctx, store.Comment{
					ID: "comment", PageID: "page", ParentID: "page", SpaceID: "space",
					Text: "API comment", Source: store.SourceAPI, Alive: true,
					RawJSON: `{}`, SyncedAt: store.NowMS(),
				}); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM record_sources
					WHERE source = 'desktop' AND payload_json LIKE '%X-Amz-%'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 3 {
					t.Fatalf("expected three retained Desktop fallback payloads, got %d", count)
				}
			}
			before := desktopRecoveryPayloads(t, st)
			md := t.TempDir()
			exported, err := (markdown.Exporter{Store: st, Dir: md}).Export(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(exported.Files) != 1 {
				t.Fatalf("Markdown fixture exported %d files", len(exported.Files))
			}
			for _, path := range exported.Files {
				body, err := os.ReadFile(path)
				if err != nil || strings.Contains(string(body), "X-Amz-") {
					t.Fatalf("Markdown retained a signed capability: %v", err)
				}
			}
			repo := t.TempDir()
			result, err := Publish(ctx, st, PublishOptions{RepoPath: repo, MarkdownDir: md})
			if err != nil {
				t.Fatal(err)
			}
			if result.Manifest.RecordSources == nil {
				t.Fatal("fallback payload export missing")
			}
			tables := append(append([]TableManifest{}, result.Manifest.Tables...), *result.Manifest.RecordSources)
			for _, table := range tables {
				payload := readDesktopExportTable(t, filepath.Join(repo, table.Path))
				if strings.Contains(payload, "X-Amz-") {
					t.Fatalf("table %s retained a signed capability", table.Name)
				}
				if table.Name == "raw_records" || table.Name == "collections" {
					for _, exact := range []string{"9223372036854775807", "0.10000000000000001", "ordinary user text"} {
						if !strings.Contains(payload, exact) {
							t.Fatalf("table %s changed exact numeric/user data %q", table.Name, exact)
						}
					}
				}
				if table.Name == "raw_records" && !strings.Contains(payload, "9007199254740993") {
					t.Fatal("Desktop outer integer rounded")
				}
				if mode == "Desktop fallback" && table.Name == "record_sources" &&
					!strings.Contains(payload, "9223372036854775807") {
					t.Fatal("fallback payload lost exact numeric data")
				}
			}
			publishedPages := 0
			if err := filepath.WalkDir(filepath.Join(repo, "pages"), func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.Contains(string(body), "X-Amz-") {
					t.Fatal("published Markdown retained a signed capability")
				}
				publishedPages++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if publishedPages != len(exported.Files) {
				t.Fatal("published Markdown copy missing")
			}
			if after := desktopRecoveryPayloads(t, st); !reflect.DeepEqual(before, after) {
				t.Fatal("export changed local Desktop recovery bytes")
			}
		})
	}
}

func desktopSignedExportFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "desktop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.Exec(`CREATE TABLE block (
		id TEXT PRIMARY KEY, space_id TEXT, type TEXT, properties TEXT, content TEXT,
		collection_id TEXT, created_time INTEGER, last_edited_time INTEGER, parent_id TEXT,
		parent_table TEXT, alive INTEGER, format TEXT);
		CREATE TABLE collection (
		id TEXT PRIMARY KEY, space_id TEXT, parent_id TEXT, parent_table TEXT,
		name TEXT, schema TEXT, format TEXT, alive INTEGER);
		CREATE TABLE comment (
		id TEXT PRIMARY KEY, parent_id TEXT, space_id TEXT, text TEXT, content TEXT,
		created_by_id TEXT, created_time INTEGER, last_edited_time INTEGER, alive INTEGER);`); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "https", Host: "files.example", Path: "/attachment"}
	u.RawQuery = url.Values{"X-Amz-Signature": {"redacted"}, "X-Amz-Credential": {"placeholder"}, "download": {"1"}}.Encode()
	format, err := json.Marshal(map[string]string{"display_source": u.String()})
	if err != nil {
		t.Fatal(err)
	}
	properties := `{"title":[["Files"]],"number":9223372036854775807,"fraction":0.10000000000000001,"format":"ordinary user text","raw_json":"not JSON"}`
	if _, err := db.Exec(`INSERT INTO block VALUES
		('page','space','page',?,'["file"]','',9007199254740993,9007199254740993,'','',1,?),
		('file','space','file',?,'[]','',9007199254740993,9007199254740993,'page','block',1,?)`,
		properties, string(format), properties, string(format)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO collection VALUES
		('collection','space','page','block','Collection',?,?,1)`, properties, string(format)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comment VALUES
		('comment','page','space','Comment',?,'user',1,1,1)`, string(format)); err != nil {
		t.Fatal(err)
	}
	return path
}

func desktopRecoveryPayloads(t *testing.T, st *store.Store) map[string]string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT 'page:' || id, raw_json FROM pages
		UNION ALL SELECT 'block:' || id, raw_json FROM blocks
		UNION ALL SELECT 'collection:' || id, raw_json FROM collections
		UNION ALL SELECT 'comment:' || id, raw_json FROM comments
		UNION ALL SELECT 'raw:' || source || ':' || record_table || ':' || record_id, raw_json FROM raw_records
		UNION ALL SELECT 'fallback:' || source || ':' || record_table || ':' || record_id, COALESCE(payload_json,'') FROM record_sources`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	signed := false
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			t.Fatal(err)
		}
		out[key] = raw
		signed = signed || strings.Contains(raw, "X-Amz-")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !signed {
		t.Fatal("local recovery fixture contains no original signed URL")
	}
	return out
}

func readDesktopExportTable(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	body, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
