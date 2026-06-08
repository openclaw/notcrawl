package share

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

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
}

type PublishSummary struct {
	Manifest  Manifest
	Committed bool
	Pushed    bool
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
	if err := ensureRepo(ctx, opts.RepoPath, opts.Remote, opts.Branch); err != nil {
		return PublishSummary{}, err
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
	pagesKeep := map[string]bool{}
	if opts.MarkdownDir != "" {
		var err error
		pagesKeep, err = copyDir(opts.MarkdownDir, pagesRoot)
		if err != nil && !os.IsNotExist(err) {
			return PublishSummary{}, err
		}
	}
	if err := pruneGeneratedFiles(pagesRoot, pagesKeep, func(path string) bool {
		return strings.HasSuffix(path, ".md")
	}); err != nil {
		return PublishSummary{}, err
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
	if opts.Push {
		if err := mirror.Push(ctx, mirror.Options{RepoPath: opts.RepoPath, Remote: opts.Remote, Branch: opts.Branch}); err != nil {
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
		if err := validateManifestFile(repoPath, table.Path); err != nil {
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
		if err := validateManifestFile(repoPath, manifest.RecordSources.Path); err != nil {
			return fmt.Errorf("record_sources snapshot: %w", err)
		}
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
	if branch == "" {
		branch = "main"
	}
	if err := pullForUpdate(ctx, repoPath, remote, branch); err != nil {
		return Manifest{}, err
	}
	return Import(ctx, st, repoPath)
}

func exportTable(ctx context.Context, db *sql.DB, repoPath, table string) (TableManifest, error) {
	path := filepath.Join("data", table+".jsonl.gz")
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
	if err := mirror.EnsureRepo(ctx, mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch}); err != nil {
		return err
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return nil
	}
	if err := runGit(ctx, repoPath, "remote", "set-url", "origin", remote); err != nil {
		if strings.Contains(err.Error(), "No such remote") {
			return runGit(ctx, repoPath, "remote", "add", "origin", remote)
		}
		return err
	}
	return nil
}

func pullForUpdate(ctx context.Context, repoPath, remote, branch string) error {
	if strings.TrimSpace(remote) != "" {
		return mirror.Pull(ctx, mirror.Options{RepoPath: repoPath, Remote: remote, Branch: branch})
	}
	if err := ensureRepo(ctx, repoPath, "", branch); err != nil {
		return err
	}
	return runGit(ctx, repoPath, "pull", "--ff-only", "origin", branch)
}

func commitGenerated(ctx context.Context, repoPath, message string) (bool, error) {
	if message == "" {
		message = "archive: notcrawl snapshot"
	}
	if err := runGit(ctx, repoPath, "add", "--", "manifest.json", "data", "pages"); err != nil {
		return false, err
	}
	staged, err := hasStagedGeneratedChanges(ctx, repoPath)
	if err != nil {
		return false, err
	}
	if !staged {
		return false, nil
	}
	if err := runGit(ctx, repoPath,
		"-c", "commit.gpgsign=false",
		"-c", "user.name=crawlkit",
		"-c", "user.email=crawlkit@example.invalid",
		"commit", "-m", message, "--", "manifest.json", "data", "pages",
	); err != nil {
		return false, err
	}
	return true, nil
}

func hasStagedGeneratedChanges(ctx context.Context, repoPath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "diff", "--cached", "--quiet", "--exit-code", "--", "manifest.json", "data", "pages")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached: %w\n%s", err, strings.TrimSpace(string(out)))
}

func runGit(ctx context.Context, dir string, args ...string) error {
	return run(ctx, dir, "git", append([]string{"-C", dir}, args...)...)
}

func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyDir(src, dst string) (map[string]bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", src)
	}
	keep := map[string]bool{}
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		keep[filepath.Clean(target)] = true
		return nil
	})
	return keep, err
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
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
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
