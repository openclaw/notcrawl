package tableexport

import (
	"bytes"
	"context"
	"encoding/csv"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestExportPreservesPropertiesNamedLikeMetadata(t *testing.T) {
	for _, schema := range []string{
		`{}`,
		`{"page_id":{"name":"page_id","type":"rich_text"},"page_title":{"name":"page_title","type":"rich_text"},"url":{"name":"url","type":"url"}}`,
	} {
		t.Run(schema, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.UpsertCollection(ctx, store.Collection{ID: "db", SchemaJSON: schema, Source: "api"}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertPage(ctx, store.Page{
				ID: "page", Title: "Page title", URL: "https://example.com/page", CollectionID: "db", Alive: true, Source: "api",
				PropertiesJSON: `{"page_id":{"type":"rich_text","rich_text":[{"plain_text":"Property ID"}]},"page_title":{"type":"rich_text","rich_text":[{"plain_text":"Property title"}]},"url":{"type":"url","url":"https://example.com/property"}}`,
			}); err != nil {
				t.Fatal(err)
			}
			for _, format := range []Format{FormatCSV, FormatTSV} {
				var out bytes.Buffer
				summary, err := (Exporter{Store: st}).Export(ctx, "db", format, &out)
				if err != nil {
					t.Fatal(err)
				}
				reader := csv.NewReader(&out)
				if format == FormatTSV {
					reader.Comma = '\t'
				}
				rows, err := reader.ReadAll()
				if err != nil {
					t.Fatal(err)
				}
				want := [][]string{
					{"page_id", "page_title", "url", "page_id (page_id)", "page_title (page_title)", "url (url)"},
					{"page", "Page title", "https://example.com/page", "Property ID", "Property title", "https://example.com/property"},
				}
				if !reflect.DeepEqual(rows, want) || summary.Columns != 6 || summary.Rows != 1 {
					t.Fatalf("%s export = %#v, summary = %+v; want %#v", format, rows, summary, want)
				}
			}
		})
	}
}

func TestSchemaPropertiesOrdersDuplicateHeadersByKey(t *testing.T) {
	const schema = `{"z":{"name":"Value","type":"rich_text"},"b":{"name":"Name","type":"title"},"a":{"name":"Name","type":"title"},"y":{"name":"Value","type":"number"}}`
	want := []exportColumn{{Key: "a", Header: "Name"}, {Key: "b", Header: "Name"}, {Key: "y", Header: "Value"}, {Key: "z", Header: "Value"}}
	for range 100 {
		if got := schemaProperties(schema); !reflect.DeepEqual(got, want) {
			t.Fatalf("schema columns = %#v; want %#v", got, want)
		}
	}
}
