package markdown

import (
	"strings"
	"testing"
)

func TestWriteKVEscapesFrontMatter(t *testing.T) {
	for _, tt := range []struct {
		name, value, want string
	}{
		{"plain", "Notes", `title: "Notes"`},
		{"backslashes", `C:\notes\queue`, `title: "C:\\notes\\queue"`},
		{"quotes after backslash", `a\"b`, `title: "a\\\"b"`},
		{"trailing backslash", `path\`, `title: "path\\"`},
		{"tab", "a\tb", `title: "a\tb"`},
		{"carriage return", "a\rb", `title: "a\rb"`},
		{"control bytes", "a\x00\x01\x7fb", `title: "a\x00\x01\x7fb"`},
		{"next line", "a\u0085b", `title: "a\u0085b"`},
		{"line separator", "a\u2028b", `title: "a\u2028b"`},
		{"paragraph separator", "a\u2029b", `title: "a\u2029b"`},
		{"unicode", "日本語 🦞", `title: "日本語 🦞"`},
		{"folded newline", "a\nb", `title: "a b"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			writeKV(&b, "title", tt.value)
			if got := b.String(); got != tt.want+"\n" {
				t.Fatalf("front matter = %q, want %q", got, tt.want+"\n")
			}
		})
	}
}
