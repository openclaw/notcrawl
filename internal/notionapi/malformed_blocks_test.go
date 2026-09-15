package notionapi

import (
	"context"
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
							fmt.Fprint(w, `{"results":[{"id":"parent","type":"toggle","has_children":true}],"has_more":false}`)
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
