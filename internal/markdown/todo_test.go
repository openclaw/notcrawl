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
			var first, second strings.Builder
			renderBlock(&first, block, 1)
			renderBlock(&second, block, 1)
			want := "  - " + tc.marker + " task\n\n"
			if first.String() != want || second.String() != want {
				t.Fatalf("rendered %q / %q, want %q", first.String(), second.String(), want)
			}
		})
	}
}
