package share

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/crawlkit/mirror"
	"github.com/openclaw/notcrawl/internal/store"
)

var exportTables = []string{
	"spaces",
	"users",
	"teams",
	"pages",
	"blocks",
	"collections",
	"comments",
	"raw_records",
	"sync_state",
}

type Manifest struct {
	GeneratedAt   string          `json:"generated_at"`
	Tables        []TableManifest `json:"tables"`
	RecordSources *TableManifest  `json:"record_sources,omitempty"`
}

type TableManifest struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Rows int    `json:"rows"`
}

type PublishOptions struct {
	RepoPath    string
	Remote      string
	Branch      string
	MarkdownDir string
	Message     string
	Push        bool
	Commit      bool
	Tag         string
}

type PublishSummary struct {
	Manifest  Manifest
	Committed bool
	Pushed    bool
	Tag       string
}

type ImportOptions struct {
	Restore         bool
	RetainRevisions bool
}

type ImportResult struct {
	Manifest  Manifest
	Mode      string
	Revisions int
}

func SubscribeWithOptions(ctx context.Context, st *store.Store, remote, repoPath, branch string, importOpts ImportOptions) (ImportResult, error) {
	if remote == "" {
		return ImportResult{}, fmt.Errorf("missing share remote")
	}
	if branch == "" {
		branch = "main"
	}
	if err := mirror.Pull(ctx, mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch}); err != nil {
		return ImportResult{}, err
	}
	return ImportWithOptions(ctx, st, repoPath, importOpts)
}

func UpdateAtWithOptions(ctx context.Context, st *store.Store, remote, repoPath, branch, ref string, importOpts ImportOptions) (ImportResult, string, error) {
	if branch == "" {
		branch = "main"
	}
	if strings.TrimSpace(ref) != "" {
		opts := mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch}
		if err := mirror.Fetch(ctx, opts); err != nil {
			return ImportResult{}, "", err
		}
		return importAtRef(ctx, st, opts, ref, importOpts)
	}
	if err := pullForUpdate(ctx, repoPath, remote, branch); err != nil {
		return ImportResult{}, "", err
	}
	result, err := ImportWithOptions(ctx, st, repoPath, importOpts)
	return result, "", err
}

func importAtRef(ctx context.Context, st *store.Store, opts mirror.Options, ref string, importOpts ImportOptions) (ImportResult, string, error) {
	body, commit, err := mirror.ReadFileAt(ctx, opts, ref, "manifest.json")
	if err != nil {
		return ImportResult{}, "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return ImportResult{}, "", err
	}
	for i := range manifest.Tables {
		manifest.Tables[i].Path = filepath.ToSlash(manifest.Tables[i].Path)
	}
	if manifest.RecordSources != nil {
		manifest.RecordSources.Path = filepath.ToSlash(manifest.RecordSources.Path)
	}
	if err := validateManifestShape(manifest); err != nil {
		return ImportResult{Manifest: manifest}, "", err
	}
	temp, err := os.MkdirTemp("", "notcrawl-share-ref-*")
	if err != nil {
		return ImportResult{Manifest: manifest}, "", err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ImportResult{Manifest: manifest}, "", err
	}
	if err := os.WriteFile(filepath.Join(temp, "manifest.json"), append(manifestBody, '\n'), 0o600); err != nil {
		return ImportResult{Manifest: manifest}, "", err
	}
	tables := append([]TableManifest(nil), manifest.Tables...)
	if manifest.RecordSources != nil {
		tables = append(tables, *manifest.RecordSources)
	}
	for _, table := range tables {
		data, resolved, err := mirror.ReadFileAt(ctx, opts, commit, table.Path)
		if err != nil {
			return ImportResult{Manifest: manifest}, "", err
		}
		if resolved != commit {
			return ImportResult{Manifest: manifest}, "", fmt.Errorf("share ref changed while reading %s", table.Path)
		}
		target := filepath.Join(temp, filepath.FromSlash(table.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return ImportResult{Manifest: manifest}, "", err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return ImportResult{Manifest: manifest}, "", err
		}
	}
	imported, err := ImportWithOptions(ctx, st, temp, importOpts)
	return imported, commit, err
}

func pullForUpdate(ctx context.Context, repoPath, remote, branch string) error {
	if strings.TrimSpace(remote) != "" {
		return mirror.Pull(ctx, mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch})
	}
	return mirror.PullCurrent(ctx, mirror.Options{RepoPath: repoPath, Branch: branch})
}
