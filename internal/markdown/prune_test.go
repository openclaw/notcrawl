package markdown

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrunePreservesMarkdownWithoutGeneratedFrontMatter(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"quoted export", "# Export example\n\n```yaml\n---\ngenerated_by: \"notcrawl\"\n---\n```\n"},
		{"marker in body", "---\ntitle: Notes\n---\ngenerated_by: \"notcrawl\"\n"},
		{"unterminated header", "---\ngenerated_by: \"notcrawl\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.md")
			if err := os.WriteFile(path, []byte(tc.text), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := pruneStaleMarkdown(dir, nil); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("unrelated Markdown was removed: %v", err)
			}
			if string(got) != tc.text {
				t.Fatalf("unrelated Markdown changed: %q", got)
			}
		})
	}
}
