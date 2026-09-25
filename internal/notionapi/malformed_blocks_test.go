package notionapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/markdown"
)

func TestMalformedBlockListingPreservesArchive(t *testing.T) {
	for _, response := range []string{
		`null`,
		`{}`,
		`{"results":[]}`,
		`{"results":[],"has_more":null}`,
		`{"results":[],"has_more":"false"}`,
		`{"results":[],"has_more":0}`,
		`{"has_more":false}`,
		`{"results":null,"has_more":false}`,
		`{"results":{},"has_more":false}`,
		`{"results":[null],"has_more":false}`,
		`{"results":[42],"has_more":false}`,
		`{"results":[{}],"has_more":false}`,
		`{"results":[{"id":" "}],"has_more":false}`,
		`{"results":[{"id":42}],"has_more":false}`,
		`{"results":[{"id":"partial","type":"paragraph"},null],"has_more":false}`,
	} {
		for _, location := range []string{"root", "later-page", "nested"} {
			t.Run(location+"/"+response, func(t *testing.T) {
				ctx := context.Background()
				fixture := &discoveryFixture{pages: []string{"first", "omitted"}}
				client, st := newDiscoveryTest(t, fixture)
				if _, err := client.Sync(ctx, st); err != nil {
					t.Fatal(err)
				}
				fixture.pages = []string{"first"}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/blocks/") {
						if location == "later-page" && r.URL.RawQuery == "page_size=100" {
							fmt.Fprint(w, `{"results":[],"has_more":true,"next_cursor":"next"}`)
						} else if location == "nested" && r.URL.Path == "/blocks/first/children" {
							fmt.Fprint(w, `{"results":[{"id":"parent","type":"toggle","toggle":{},"has_children":true}],"has_more":false}`)
						} else {
							fmt.Fprint(w, response)
						}
						return
					}
					fixture.serve(w, r)
				}))
				t.Cleanup(server.Close)
				client.BaseURL, client.HTTP = server.URL, server.Client()
				if _, err := client.Sync(ctx, st); err == nil {
					t.Error("malformed block listing reported success")
				}
				var partial int
				if err := st.DB().QueryRow("select count(*) from blocks where id = 'partial'").Scan(&partial); err != nil || partial != 0 {
					t.Errorf("malformed batch was partially written: %d, %v", partial, err)
				}
				live, err := st.RecordHasLiveSource(ctx, "block", "firstblock", SourceName)
				if err != nil || !live {
					t.Errorf("cached block retired: live=%v, err=%v", live, err)
				}
				complete, err := st.HasSyncState(ctx, SourceName, "page_blocks", "first")
				if err != nil || complete {
					t.Errorf("malformed page marked complete: %v, %v", complete, err)
				}
				results, err := st.Search(ctx, "firstbody", 10)
				if err != nil || len(results) != 1 {
					t.Errorf("cached block lost from search: %v, %v", results, err)
				}
				assertDiscoveryPage(t, st, "omitted", true)
				exported, err := (markdown.Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var content strings.Builder
				for _, path := range exported.Files {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					content.Write(data)
				}
				if !strings.Contains(content.String(), "firstbody") || !strings.Contains(content.String(), "omittedbody") {
					t.Error("cached body lost from Markdown")
				}
			})
		}
	}
}

func TestIncompleteBlockPreservesArchive(t *testing.T) {
	for _, malformed := range []struct {
		name, field string
		value       any
		omit        bool
	}{
		{name: "id-only"},
		{name: "missing-type", field: "type", omit: true},
		{name: "null-type", field: "type"},
		{name: "numeric-type", field: "type", value: 42},
		{name: "boolean-type", field: "type", value: false},
		{name: "empty-type", field: "type", value: ""},
		{name: "blank-type", field: "type", value: " "},
		{name: "missing-body", field: "paragraph", omit: true},
		{name: "null-body", field: "paragraph"},
		{name: "string-body", field: "paragraph", value: "replacement"},
		{name: "array-body", field: "paragraph", value: []any{}},
		{name: "numeric-body", field: "paragraph", value: 42},
		{name: "boolean-body", field: "paragraph", value: false},
		{name: "missing-children", field: "has_children", omit: true},
		{name: "null-children", field: "has_children"},
		{name: "string-children", field: "has_children", value: "true"},
		{name: "numeric-children", field: "has_children", value: 1},
		{name: "null-archived", field: "archived"},
		{name: "string-archived", field: "archived", value: "false"},
		{name: "null-trash", field: "in_trash"},
		{name: "string-trash", field: "in_trash", value: "false"},
	} {
		for _, location := range []string{"root", "later-page", "nested"} {
			t.Run(location+"/"+malformed.name, func(t *testing.T) {
				ctx := context.Background()
				fixture := &discoveryFixture{pages: []string{"first", "omitted"}}
				client, st := newDiscoveryTest(t, fixture)
				block := func(id string) obj {
					return obj{
						"object": "block", "id": id, "type": "paragraph",
						"has_children": id == "parent" || id == "child",
						"paragraph":    obj{"rich_text": []any{obj{"plain_text": id + "body"}}},
					}
				}
				incomplete := false
				incompleteServed := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					child, ok := map[string]string{
						"/blocks/first/children":  "parent",
						"/blocks/parent/children": "child",
						"/blocks/child/children":  "leaf",
					}[r.URL.Path]
					if !ok {
						fixture.serve(w, r)
						return
					}
					response := obj{"results": []any{block(child)}, "has_more": false}
					target := "parent"
					if location == "nested" {
						target = "child"
					}
					if incomplete && child == target {
						if location == "later-page" && r.URL.Query().Get("start_cursor") == "" {
							response = obj{"results": []any{block("progress")}, "has_more": true, "next_cursor": "next"}
						} else {
							incompleteServed = true
							item := block(target)
							item["paragraph"] = obj{"rich_text": []any{obj{"plain_text": "replacement"}}}
							if malformed.name == "id-only" {
								item = obj{"id": target}
							} else if malformed.omit {
								delete(item, malformed.field)
							} else {
								item[malformed.field] = malformed.value
							}
							response["results"] = []any{block("partial"), item}
						}
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
				t.Cleanup(server.Close)
				client.BaseURL, client.HTTP = server.URL, server.Client()
				if _, err := client.Sync(ctx, st); err != nil {
					t.Fatal(err)
				}
				incomplete = true
				fixture.pages = []string{"first"}
				if _, err := client.Sync(ctx, st); err == nil {
					t.Error("incomplete block reported success")
				}
				if !incompleteServed {
					t.Fatal("did not reach incomplete block")
				}
				var partial int
				if err := st.DB().QueryRow("select count(*) from blocks where id = 'partial'").Scan(&partial); err != nil || partial != 0 {
					t.Errorf("invalid batch was partially written: %d, %v", partial, err)
				}
				for _, id := range []string{"parent", "child", "leaf"} {
					var text string
					if err := st.DB().QueryRow("select text from blocks where id = ?", id).Scan(&text); err != nil || text != id+"body" {
						t.Errorf("cached %s overwritten: %q, %v", id, text, err)
					}
					live, err := st.RecordHasLiveSource(ctx, "block", id, SourceName)
					if err != nil || !live {
						t.Errorf("cached %s retired: %v, %v", id, live, err)
					}
					results, err := st.Search(ctx, id+"body", 10)
					if err != nil || len(results) != 1 {
						t.Errorf("cached %s lost from search: %v, %v", id, results, err)
					}
				}
				if location == "later-page" {
					results, err := st.Search(ctx, "progressbody", 10)
					if err != nil || len(results) != 1 {
						t.Errorf("previously committed batch lost: %v, %v", results, err)
					}
				}
				complete, err := st.HasSyncState(ctx, SourceName, "page_blocks", "first")
				if err != nil || complete {
					t.Errorf("incomplete page marked complete: %v, %v", complete, err)
				}
				assertDiscoveryPage(t, st, "omitted", true)
				exported, err := (markdown.Exporter{Store: st, Dir: t.TempDir()}).Export(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var content strings.Builder
				for _, path := range exported.Files {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					content.Write(data)
				}
				for _, text := range []string{"parentbody", "childbody", "leafbody", "omittedbody"} {
					if !strings.Contains(content.String(), text) {
						t.Errorf("cached %s lost from Markdown", text)
					}
				}
			})
		}
	}
}

func TestBlockListingAcceptsEmptyBodies(t *testing.T) {
	for _, typ := range []string{"divider", "breadcrumb", "unsupported", "future_type", "paragraph"} {
		t.Run(typ, func(t *testing.T) {
			client, st := newDiscoveryTest(t, &discoveryFixture{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(obj{
					"results":  []any{obj{"id": "empty", "type": typ, typ: obj{}, "has_children": false}},
					"has_more": false,
				})
			}))
			t.Cleanup(server.Close)
			client.BaseURL, client.HTTP = server.URL, server.Client()
			count, warnings, err := client.walkBlocks(context.Background(), st, "page", "page", "")
			if err != nil || count != 1 || len(warnings) != 0 {
				t.Fatalf("empty body: count=%d warnings=%v error=%v", count, warnings, err)
			}
		})
	}
}
