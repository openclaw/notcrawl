package markdown

import (
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestCodePreservesWhitespaceAndFences(t *testing.T) {
	for _, tc := range []struct{ text, fence string }{
		{"  indented  \n\n", "```"},
		{"\tline\t", "```"},
		{"\n", "```"},
		{"", "```"},
		{"```\n", "````"},
		{"a````b\n``c", "`````"},
	} {
		var b strings.Builder
		renderBlock(&b, store.Block{Type: "code", Text: tc.text}, "")
		want := tc.fence + "text\n" + tc.text
		if !strings.HasSuffix(tc.text, "\n") {
			want += "\n"
		}
		want += tc.fence + "\n\n"
		if b.String() != want {
			t.Errorf("code %q rendered as %q; want %q", tc.text, b.String(), want)
		}
	}
}
