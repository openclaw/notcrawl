package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/mirror"
	cksnapshot "github.com/openclaw/crawlkit/snapshot"
	"github.com/openclaw/notcrawl/internal/store"
)

func Publish(ctx context.Context, st *store.Store, opts PublishOptions) (PublishSummary, error) {
	if opts.RepoPath == "" {
		return PublishSummary{}, fmt.Errorf("missing share repo path")
	}
	if opts.Branch == "" {
		opts.Branch = "main"
	}
	if opts.Message == "" {
		opts.Message = "archive: notcrawl snapshot"
	}
	if strings.TrimSpace(opts.Tag) != "" && !opts.Commit {
		return PublishSummary{}, fmt.Errorf("snapshot tag requires a commit")
	}
	if err := ensureRepo(ctx, opts.RepoPath, opts.Remote, opts.Branch); err != nil {
		return PublishSummary{}, err
	}
	if err := mirror.ValidateTag(ctx, mirror.Options{RepoPath: opts.RepoPath, Remote: opts.Remote, Branch: opts.Branch}, opts.Tag); err != nil {
		return PublishSummary{}, err
	}
	if opts.Push {
		if err := mirror.SyncForWrite(ctx, mirror.Options{RepoPath: opts.RepoPath, Remote: opts.Remote, Branch: opts.Branch, DirMode: 0o750}); err != nil {
			return PublishSummary{}, err
		}
	}
	dataRoot := filepath.Join(opts.RepoPath, "data")
	pagesRoot := filepath.Join(opts.RepoPath, "pages")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		return PublishSummary{}, err
	}
	if err := os.MkdirAll(pagesRoot, 0o755); err != nil {
		return PublishSummary{}, err
	}
	manifest := Manifest{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	dataKeep := map[string]bool{}
	for _, table := range exportTables {
		tm, err := exportTable(ctx, st.DB(), opts.RepoPath, table)
		if err != nil {
			return PublishSummary{}, err
		}
		manifest.Tables = append(manifest.Tables, tm)
		dataKeep[filepath.Clean(filepath.Join(opts.RepoPath, tm.Path))] = true
	}
	recordSources, err := exportTable(ctx, st.DB(), opts.RepoPath, "record_sources")
	if err != nil {
		return PublishSummary{}, err
	}
	manifest.RecordSources = &recordSources
	dataKeep[filepath.Clean(filepath.Join(opts.RepoPath, recordSources.Path))] = true
	if err := pruneGeneratedFiles(dataRoot, dataKeep, func(path string) bool {
		return strings.HasSuffix(path, ".jsonl.gz")
	}); err != nil {
		return PublishSummary{}, err
	}
	pagesSynced := false
	if opts.MarkdownDir != "" {
		_, err := cksnapshot.SyncSidecarTree(ctx, cksnapshot.SidecarTreeOptions{
			SourceDir: opts.MarkdownDir,
			RootDir:   opts.RepoPath,
			TargetDir: "pages",
			Kind:      "markdown",
			Prune:     func(path string) bool { return strings.HasSuffix(path, ".md") },
		})
		if err == nil {
			pagesSynced = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return PublishSummary{}, err
		}
	}
	if !pagesSynced {
		if err := pruneGeneratedFiles(pagesRoot, map[string]bool{}, func(path string) bool {
			return strings.HasSuffix(path, ".md")
		}); err != nil {
			return PublishSummary{}, err
		}
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return PublishSummary{}, err
	}
	if err := os.WriteFile(filepath.Join(opts.RepoPath, "manifest.json"), append(b, '\n'), 0o644); err != nil {
		return PublishSummary{}, err
	}
	s := PublishSummary{Manifest: manifest}
	if opts.Commit {
		committed, err := commitGenerated(ctx, opts.RepoPath, opts.Message)
		if err != nil {
			return s, err
		}
		s.Committed = committed
	}
	if strings.TrimSpace(opts.Tag) != "" {
		tag, err := mirror.CreateImmutableTag(ctx, mirror.Options{RepoPath: opts.RepoPath, Remote: opts.Remote, Branch: opts.Branch}, opts.Tag)
		if err != nil {
			return s, err
		}
		s.Tag = tag
	}
	if opts.Push {
		mirrorOpts := mirror.Options{RepoPath: opts.RepoPath, Remote: opts.Remote, Branch: opts.Branch}
		var err error
		if strings.TrimSpace(opts.Tag) == "" {
			err = mirror.Push(ctx, mirrorOpts)
		} else {
			err = mirror.PushSnapshot(ctx, mirrorOpts, opts.Tag)
		}
		if err != nil {
			return s, err
		}
		s.Pushed = true
	}
	return s, nil
}

func ensureRepo(ctx context.Context, repoPath, remote, branch string) error {
	opts := mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch, DirMode: 0o750}
	if strings.TrimSpace(remote) != "" {
		return mirror.EnsureRemote(ctx, opts)
	}
	return mirror.EnsureRepo(ctx, opts)
}

func commitGenerated(ctx context.Context, repoPath, message string) (bool, error) {
	if message == "" {
		message = "archive: notcrawl snapshot"
	}
	// Git cannot commit an empty directory pathspec. Keep pages addressable
	// even before the first page, or after the last generated page is removed.
	marker, err := os.OpenFile(filepath.Join(repoPath, "pages", ".gitkeep"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		if err := marker.Close(); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return false, err
	}
	return mirror.CommitPaths(ctx, mirror.Options{RepoPath: repoPath}, message, []string{"manifest.json", "data", "pages"})
}

func pruneGeneratedFiles(root string, keep map[string]bool, shouldPrune func(string) bool) error {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		clean := filepath.Clean(path)
		if shouldPrune(clean) && !keep[clean] {
			return os.Remove(clean)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}
