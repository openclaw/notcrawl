package markdown

import (
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestCodePreservesWhitespace(t *testing.T) {
	for _, text := range []string{"  indented  \n\n", "\tline\t", "\n", ""} {
		var b strings.Builder
		renderBlock(&b, store.Block{Type: "code", Text: text}, "")
		want := "```text\n" + text
		if !strings.HasSuffix(text, "\n") {
			want += "\n"
		}
		want += "```\n\n"
		if b.String() != want {
			t.Errorf("code %q rendered as %q; want %q", text, b.String(), want)
		}
	}
}
