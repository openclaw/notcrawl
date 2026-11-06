package markdown

import (
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestRenderTodoCheckedState(t *testing.T) {
	for _, tc := range []struct {
		name, source, properties, marker string
	}{
		{"api checked", store.SourceAPI, `{"checked":true}`, "[x]"},
		{"api unchecked", store.SourceAPI, `{"checked":false}`, "[ ]"},
		{"desktop checked", store.SourceDesktop, `{"checked":[["Yes"]]}`, "[x]"},
		{"desktop unchecked", store.SourceDesktop, `{"checked":[["No"]]}`, "[ ]"},
		{"missing", store.SourceAPI, `{}`, "[ ]"},
		{"malformed", store.SourceAPI, `{"checked":"true"}`, "[ ]"},
		{"invalid", store.SourceDesktop, `{`, "[ ]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := store.Block{Type: "to_do", Text: "task", Source: tc.source, PropertiesJSON: tc.properties}
			var b strings.Builder
			renderBlock(&b, block, "  ")
			want := "  - " + tc.marker + " task\n\n"
			if b.String() != want {
				t.Fatalf("rendered %q, want %q", b.String(), want)
			}
		})
	}
}
