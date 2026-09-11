package share

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/notcrawl/internal/store"
)

func TestPublishCommitsEmptyMarkdownArchive(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := filepath.Join(t.TempDir(), "share")
	for i := 0; i < 2; i++ {
		if _, err := Publish(ctx, st, PublishOptions{RepoPath: repo, Commit: true}); err != nil {
			t.Fatal(err)
		}
	}
	files := gitOutputForTest(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(files, "manifest.json") || !strings.Contains(files, "data/pages.jsonl.gz") {
		t.Fatalf("snapshot is incomplete: %s", files)
	}
	if got := gitOutputForTest(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("publish left uncommitted changes: %s", got)
	}
}

func TestPublishCommitsRemovalOfLastMarkdownPage(t *testing.T) {
	ctx := context.Background()
	st, md := snapshotStoreForTest(t, ctx, "Page", "fixture")
	defer st.Close()
	repo := filepath.Join(t.TempDir(), "share")
	if _, err := Publish(ctx, st, PublishOptions{RepoPath: repo, MarkdownDir: md, Commit: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("unrelated staged notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, repo, "add", "notes.txt")
	if _, err := Publish(ctx, st, PublishOptions{RepoPath: repo, Commit: true}); err != nil {
		t.Fatal(err)
	}
	files := gitOutputForTest(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(files, ".md") || strings.Contains(files, "notes.txt") {
		t.Fatalf("unexpected committed file: %s", files)
	}
	if got := gitOutputForTest(t, repo, "diff", "--cached", "--name-only"); strings.TrimSpace(got) != "notes.txt" {
		t.Fatalf("unrelated staged change was altered: %s", got)
	}
}
