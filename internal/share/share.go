package share

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
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

func Import(ctx context.Context, st *store.Store, repoPath string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(repoPath, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(repoPath, manifest); err != nil {
		return manifest, err
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return manifest, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `delete from record_sources`); err != nil {
		return manifest, err
	}
	for _, table := range exportTables {
		if _, err := tx.ExecContext(ctx, "delete from "+quoteIdent(table)); err != nil {
			return manifest, err
		}
	}
	for _, table := range manifest.Tables {
		rows, err := importTable(ctx, tx, filepath.Join(repoPath, table.Path), table.Name)
		if err != nil {
			return manifest, err
		}
		if rows != table.Rows {
			return manifest, fmt.Errorf("snapshot table %s row count mismatch: manifest=%d imported=%d", table.Name, table.Rows, rows)
		}
	}
	if manifest.RecordSources != nil {
		rows, err := importTable(ctx, tx, filepath.Join(repoPath, manifest.RecordSources.Path), "record_sources")
		if err != nil {
			return manifest, err
		}
		if rows != manifest.RecordSources.Rows {
			return manifest, fmt.Errorf("record_sources row count mismatch: manifest=%d imported=%d", manifest.RecordSources.Rows, rows)
		}
	} else {
		if err := rebuildRecordSources(ctx, tx); err != nil {
			return manifest, err
		}
	}
	if err := tx.Commit(); err != nil {
		return manifest, err
	}
	if err := st.RebuildFTS(ctx); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func validateManifest(repoPath string, manifest Manifest) error {
	if err := validateManifestShape(manifest); err != nil {
		return err
	}
	for _, table := range manifest.Tables {
		if err := validateManifestFile(repoPath, table.Path); err != nil {
			return fmt.Errorf("snapshot table %s: %w", table.Name, err)
		}
	}
	if manifest.RecordSources != nil {
		if err := validateManifestFile(repoPath, manifest.RecordSources.Path); err != nil {
			return fmt.Errorf("record_sources snapshot: %w", err)
		}
	}
	return nil
}

func validateManifestShape(manifest Manifest) error {
	expected := make(map[string]bool, len(exportTables))
	for _, table := range exportTables {
		expected[table] = true
	}
	seen := make(map[string]bool, len(manifest.Tables))
	for _, table := range manifest.Tables {
		if table.Rows < 0 {
			return fmt.Errorf("snapshot table %s has negative row count", table.Name)
		}
		if !expected[table.Name] {
			return fmt.Errorf("unsupported snapshot table %q", table.Name)
		}
		if seen[table.Name] {
			return fmt.Errorf("duplicate snapshot table %q", table.Name)
		}
		seen[table.Name] = true
		if err := validateRelativeSnapshotPath(table.Path); err != nil {
			return fmt.Errorf("snapshot table %s: %w", table.Name, err)
		}
	}
	for _, table := range exportTables {
		if !seen[table] {
			return fmt.Errorf("snapshot manifest missing required table %q", table)
		}
	}
	if manifest.RecordSources != nil {
		if manifest.RecordSources.Rows < 0 {
			return fmt.Errorf("record_sources has negative row count")
		}
		if manifest.RecordSources.Name != "record_sources" {
			return fmt.Errorf("invalid record_sources table name %q", manifest.RecordSources.Name)
		}
		if err := validateRelativeSnapshotPath(manifest.RecordSources.Path); err != nil {
			return fmt.Errorf("record_sources snapshot: %w", err)
		}
	}
	return nil
}

func validateRelativeSnapshotPath(value string) error {
	clean := filepath.Clean(strings.TrimSpace(value))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes snapshot repository: %s", value)
	}
	return nil
}

func validateManifestFile(repoPath, path string) error {
	if path == "" {
		return fmt.Errorf("missing path")
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	full, err := filepath.Abs(filepath.Join(repoPath, path))
	if err != nil {
		return err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink snapshot path is not allowed: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes snapshot repository: %s", path)
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", path)
	}
	return nil
}

func Subscribe(ctx context.Context, st *store.Store, remote, repoPath, branch string) (Manifest, error) {
	if remote == "" {
		return Manifest{}, fmt.Errorf("missing share remote")
	}
	if branch == "" {
		branch = "main"
	}
	if err := mirror.Pull(ctx, mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch}); err != nil {
		return Manifest{}, err
	}
	return Import(ctx, st, repoPath)
}

func Update(ctx context.Context, st *store.Store, remote, repoPath, branch string) (Manifest, error) {
	manifest, _, err := UpdateAt(ctx, st, remote, repoPath, branch, "")
	return manifest, err
}

func UpdateAt(ctx context.Context, st *store.Store, remote, repoPath, branch, ref string) (Manifest, string, error) {
	if branch == "" {
		branch = "main"
	}
	if strings.TrimSpace(ref) != "" {
		opts := mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch}
		if err := mirror.Fetch(ctx, opts); err != nil {
			return Manifest{}, "", err
		}
		return importAtRef(ctx, st, opts, ref)
	}
	if err := pullForUpdate(ctx, repoPath, remote, branch); err != nil {
		return Manifest{}, "", err
	}
	manifest, err := Import(ctx, st, repoPath)
	return manifest, "", err
}

func importAtRef(ctx context.Context, st *store.Store, opts mirror.Options, ref string) (Manifest, string, error) {
	body, commit, err := mirror.ReadFileAt(ctx, opts, ref, "manifest.json")
	if err != nil {
		return Manifest{}, "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Manifest{}, "", err
	}
	for i := range manifest.Tables {
		manifest.Tables[i].Path = filepath.ToSlash(manifest.Tables[i].Path)
	}
	if manifest.RecordSources != nil {
		manifest.RecordSources.Path = filepath.ToSlash(manifest.RecordSources.Path)
	}
	if err := validateManifestShape(manifest); err != nil {
		return manifest, "", err
	}
	temp, err := os.MkdirTemp("", "notcrawl-share-ref-*")
	if err != nil {
		return manifest, "", err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, "", err
	}
	if err := os.WriteFile(filepath.Join(temp, "manifest.json"), append(manifestBody, '\n'), 0o600); err != nil {
		return manifest, "", err
	}
	tables := append([]TableManifest(nil), manifest.Tables...)
	if manifest.RecordSources != nil {
		tables = append(tables, *manifest.RecordSources)
	}
	for _, table := range tables {
		data, resolved, err := mirror.ReadFileAt(ctx, opts, commit, table.Path)
		if err != nil {
			return manifest, "", err
		}
		if resolved != commit {
			return manifest, "", fmt.Errorf("share ref changed while reading %s", table.Path)
		}
		target := filepath.Join(temp, filepath.FromSlash(table.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return manifest, "", err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return manifest, "", err
		}
	}
	imported, err := Import(ctx, st, temp)
	return imported, commit, err
}

func exportTable(ctx context.Context, db *sql.DB, repoPath, table string) (TableManifest, error) {
	path := filepath.ToSlash(filepath.Join("data", table+".jsonl.gz"))
	full := filepath.Join(repoPath, path)
	out, err := os.Create(full)
	if err != nil {
		return TableManifest{}, err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	rows, err := db.QueryContext(ctx, "select * from "+quoteIdent(table))
	if err != nil {
		return TableManifest{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return TableManifest{}, err
	}
	count := 0
	enc := json.NewEncoder(gz)
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return TableManifest{}, err
		}
		row := map[string]any{}
		for i, col := range cols {
			row[col] = exportValue(values[i])
		}
		if err := enc.Encode(row); err != nil {
			return TableManifest{}, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return TableManifest{}, err
	}
	return TableManifest{Name: table, Path: path, Rows: count}, nil
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func importTable(ctx context.Context, db sqlExecer, path, table string) (int, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	if _, err := db.ExecContext(ctx, "delete from "+quoteIdent(table)); err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(gz)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 32*1024*1024)
	count := 0
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return count, err
		}
		if len(row) == 0 {
			continue
		}
		cols := make([]string, 0, len(row))
		for col := range row {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		args := make([]any, 0, len(cols))
		holders := make([]string, 0, len(cols))
		quotedCols := make([]string, 0, len(cols))
		for _, col := range cols {
			quotedCols = append(quotedCols, quoteIdent(col))
			holders = append(holders, "?")
			args = append(args, row[col])
		}
		stmt := fmt.Sprintf("insert or replace into %s(%s) values(%s)", quoteIdent(table), strings.Join(quotedCols, ","), strings.Join(holders, ","))
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			return count, err
		}
		count++
	}
	return count, scanner.Err()
}

func rebuildRecordSources(ctx context.Context, db sqlExecer) error {
	for _, stmt := range []string{
		`insert into record_sources(record_table, record_id, source, synced_at, alive)
			select 'page', id, source, synced_at, alive from pages`,
		`insert into record_sources(record_table, record_id, source, synced_at, alive)
			select 'block', id, source, synced_at, alive from blocks`,
		`insert into record_sources(record_table, record_id, source, synced_at, alive)
			select 'comment', id, source, synced_at, alive from comments`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func ensureRepo(ctx context.Context, repoPath, remote, branch string) error {
	opts := mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch, DirMode: 0o750}
	if strings.TrimSpace(remote) != "" {
		return mirror.EnsureRemote(ctx, opts)
	}
	return mirror.EnsureRepo(ctx, opts)
}

func pullForUpdate(ctx context.Context, repoPath, remote, branch string) error {
	if strings.TrimSpace(remote) != "" {
		return mirror.Pull(ctx, mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch})
	}
	return mirror.PullCurrent(ctx, mirror.Options{RepoPath: repoPath, Branch: branch})
}

func commitGenerated(ctx context.Context, repoPath, message string) (bool, error) {
	if message == "" {
		message = "archive: notcrawl snapshot"
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

func exportValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return x
	}
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
