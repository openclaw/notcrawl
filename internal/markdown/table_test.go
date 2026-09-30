package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestExportArchivedSimpleTable(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPage(ctx, store.Page{ID: "page", Title: "Table", Alive: true, Source: "api", SyncedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for _, block := range []store.Block{
		{ID: "table", ParentID: "page", Type: "table", PropertiesJSON: `{"table_width":2,"has_column_header":true}`},
		{ID: "z-header", ParentID: "table", Type: "table_row", DisplayOrder: 1, PropertiesJSON: `{"cells":[[{"plain_text":"Name"}],[{"plain_text":"Value"}]]}`},
		{ID: "a-row", ParentID: "table", Type: "table_row", DisplayOrder: 2, PropertiesJSON: `{"cells":[[{"plain_text":"moonstone"}],[{"text":{"content":"left|right"}}]]}`},
	} {
		block.PageID, block.Source, block.SyncedAt, block.Alive = "page", "api", 1, true
		if err := st.UpsertBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := (Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(summary.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	want := "| Name | Value |\n| --- | --- |\n| moonstone | left\\|right |\n"
	if !strings.Contains(string(data), want) {
		t.Errorf("missing table %q in:\n%s", want, data)
	}
	// Existing archives have empty blocks.text; maintain must recover their index.
	if _, err := st.DB().ExecContext(ctx, "delete from page_fts"); err != nil {
		t.Fatal(err)
	}
	if err := st.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	results, err := st.Search(ctx, "moonstone", 10)
	if err != nil || len(results) != 1 {
		t.Errorf("table search results=%v error=%v", results, err)
	}
}

func TestRenderSimpleTableWithoutHeader(t *testing.T) {
	var b strings.Builder
	renderBlocks(&b, "page", []store.Block{
		{ID: "table", ParentID: "page", Type: "table", PropertiesJSON: `{"table_width":2,"has_column_header":false}`},
		{ID: "row", ParentID: "table", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"first"}],[]]}`},
	})
	want := "|  |  |\n| --- | --- |\n| first |  |\n\n"
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
}

func TestRenderSimpleTableFallbacks(t *testing.T) {
	for _, properties := range []string{`{}`, `{"table_width":3}`, `{"table_width":2,"has_column_header":"invalid"}`} {
		var b strings.Builder
		renderBlocks(&b, "page", []store.Block{
			{ID: "table", ParentID: "page", Type: "table", PropertiesJSON: properties},
			{ID: "row", ParentID: "table", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"retained"}],[]]}`},
		})
		if !strings.Contains(b.String(), "[table]") || !strings.Contains(b.String(), "retained") {
			t.Fatalf("lost malformed table content: %q", b.String())
		}
	}
	var b strings.Builder
	renderBlocks(&b, "page", []store.Block{{ID: "empty", ParentID: "page", Type: "table", PropertiesJSON: `{"table_width":2}`}})
	if b.String() != "[table]\n\n" {
		t.Fatalf("empty table disappeared: %q", b.String())
	}
	b.Reset()
	renderBlocks(&b, "page", []store.Block{{ID: "row", ParentID: "missing", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"orphan"}]]}`}})
	if b.String() != "orphan\n\n" {
		t.Fatalf("orphan row disappeared: %q", b.String())
	}
}

func TestRenderNestedSimpleTableEscapesCellText(t *testing.T) {
	var b strings.Builder
	renderBlocks(&b, "page", []store.Block{
		{ID: "list", ParentID: "page", Type: "bulleted_list_item", Text: "Items"},
		{ID: "table", ParentID: "list", Type: "table", PropertiesJSON: `{"table_width":1,"has_column_header":true}`},
		{ID: "header", ParentID: "table", Type: "table_row", DisplayOrder: 1, PropertiesJSON: `{"cells":[[{"plain_text":"Label"}]]}`},
		{ID: "row", ParentID: "table", Type: "table_row", DisplayOrder: 2, PropertiesJSON: `{"cells":[[{"plain_text":"<tag> & *bold* [link] ` + "`code`" + ` a\\b\nnext"}]]}`},
	})
	want := "- Items\n\n  | Label |\n  | --- |\n  | &lt;tag&gt; &amp; \\*bold\\* \\[link\\] \\`code\\` a\\\\b next |\n\n"
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
}

func TestRenderTableUsesMarkdownContainerIndent(t *testing.T) {
	for _, tc := range []struct{ name, typ, text, indent string }{
		{"columns", "column_list", "", ""},
		{"numbered list", "numbered_list_item", "Items", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderBlocks(&b, "page", []store.Block{
				{ID: "parent", ParentID: "page", Type: tc.typ, Text: tc.text},
				{ID: "column", ParentID: "parent", Type: "column"},
				{ID: "table", ParentID: "column", Type: "table", PropertiesJSON: `{"table_width":1,"has_column_header":true}`},
				{ID: "row", ParentID: "table", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"Label"}]]}`},
			})
			want := tc.indent + "| Label |\n" + tc.indent + "| --- |\n\n"
			if tc.text != "" {
				want = "1. Items\n\n" + want
			}
			if b.String() != want {
				t.Fatalf("got %q want %q", b.String(), want)
			}
		})
	}
}

func TestRenderOrphanTableRowEscapesLiteralCells(t *testing.T) {
	var b strings.Builder
	renderBlocks(&b, "page", []store.Block{{ID: "row", ParentID: "missing", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"<tag> | *literal*"}]]}`}})
	if want := "&lt;tag&gt; \\| \\*literal\\*\n\n"; b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
}

func TestRenderQuotedSimpleTable(t *testing.T) {
	var b strings.Builder
	renderBlocks(&b, "page", []store.Block{
		{ID: "quote", ParentID: "page", Type: "quote", Text: "Quoted table"},
		{ID: "table", ParentID: "quote", Type: "table", PropertiesJSON: `{"table_width":1,"has_column_header":true}`},
		{ID: "row", ParentID: "table", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"~~literal~~"}]]}`},
	})
	want := "> Quoted table\n>\n> | \\~\\~literal\\~\\~ |\n> | --- |\n>\n"
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
}

func TestRenderTableFallbackEscapesBlockSyntax(t *testing.T) {
	for _, input := range []string{"# title", "- item", "1. item", "1) item", "~~deleted~~"} {
		var b strings.Builder
		renderBlocks(&b, "page", []store.Block{{ID: "row", ParentID: "page", Type: "table_row", PropertiesJSON: `{"cells":[[{"plain_text":"` + input + `"}]]}`}})
		if !strings.Contains(b.String(), "\\") {
			t.Errorf("unescaped fallback %q", b.String())
		}
	}
}
