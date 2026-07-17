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

type ImportOptions struct {
	Restore         bool
	RetainRevisions bool
}

type ImportResult struct {
	Manifest  Manifest
	Mode      string
	Revisions int
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
	result, err := ImportWithOptions(ctx, st, repoPath, ImportOptions{})
	return result.Manifest, err
}

func ImportWithOptions(ctx context.Context, st *store.Store, repoPath string, opts ImportOptions) (ImportResult, error) {
	b, err := os.ReadFile(filepath.Join(repoPath, "manifest.json"))
	if err != nil {
		return ImportResult{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return ImportResult{}, err
	}
	if err := validateManifest(repoPath, manifest); err != nil {
		return ImportResult{Manifest: manifest}, err
	}
	result := ImportResult{Manifest: manifest, Mode: "merge"}
	err = st.WithSQLTransaction(ctx, func(tx *sql.Tx) error {
		if opts.Restore {
			result.Mode = "restore"
			if opts.RetainRevisions {
				var err error
				result.Revisions, err = retainAllImportRevisions(ctx, tx, "snapshot-restore")
				if err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `delete from record_sources`); err != nil {
				return err
			}
			for _, table := range exportTables {
				if _, err := tx.ExecContext(ctx, "delete from "+quoteIdent(table)); err != nil {
					return err
				}
			}
		}
		for _, table := range manifest.Tables {
			rows, revisions, err := importTable(ctx, st, tx, filepath.Join(repoPath, table.Path), table.Name, opts)
			if err != nil {
				return err
			}
			result.Revisions += revisions
			if rows != table.Rows {
				return fmt.Errorf("snapshot table %s row count mismatch: manifest=%d imported=%d", table.Name, table.Rows, rows)
			}
		}
		if manifest.RecordSources != nil {
			rows, revisions, err := importTable(ctx, st, tx, filepath.Join(repoPath, manifest.RecordSources.Path), "record_sources", opts)
			if err != nil {
				return err
			}
			result.Revisions += revisions
			if rows != manifest.RecordSources.Rows {
				return fmt.Errorf("record_sources row count mismatch: manifest=%d imported=%d", manifest.RecordSources.Rows, rows)
			}
		} else if err := rebuildRecordSources(ctx, tx); err != nil {
			return err
		}
		if err := normalizeImportedTombstones(ctx, tx); err != nil {
			return err
		}
		return reconcileImportedAlive(ctx, tx)
	})
	if err != nil {
		return result, err
	}
	if err := st.RebuildFTS(ctx); err != nil {
		return result, err
	}
	return result, nil
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

func Update(ctx context.Context, st *store.Store, remote, repoPath, branch string) (Manifest, error) {
	manifest, _, err := UpdateAt(ctx, st, remote, repoPath, branch, "")
	return manifest, err
}

func UpdateAt(ctx context.Context, st *store.Store, remote, repoPath, branch, ref string) (Manifest, string, error) {
	result, resolved, err := UpdateAtWithOptions(ctx, st, remote, repoPath, branch, ref, ImportOptions{})
	return result.Manifest, resolved, err
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

var importPrimaryKeys = map[string][]string{
	"spaces":         {"id"},
	"users":          {"id"},
	"teams":          {"id"},
	"pages":          {"id"},
	"blocks":         {"id"},
	"collections":    {"id"},
	"comments":       {"id"},
	"raw_records":    {"source", "record_table", "record_id"},
	"sync_state":     {"source", "entity_type", "entity_id"},
	"record_sources": {"record_table", "record_id", "source"},
}

var revisionTables = []string{
	"spaces",
	"users",
	"teams",
	"pages",
	"blocks",
	"collections",
	"comments",
	"raw_records",
	"record_sources",
}

func importTable(ctx context.Context, st *store.Store, db *sql.Tx, path, table string, opts ImportOptions) (int, int, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return 0, 0, err
	}
	defer gz.Close()
	scanner := bufio.NewScanner(gz)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 32*1024*1024)
	count := 0
	revisions := 0
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return count, revisions, err
		}
		if len(row) == 0 {
			continue
		}
		count++
		if _, err := importRecordKey(table, row); err != nil {
			return count, revisions, err
		}
		if !opts.Restore {
			preserve, err := preserveLocalTombstone(ctx, db, table, row)
			if err != nil {
				return count, revisions, err
			}
			if preserve {
				continue
			}
		}
		var previous map[string]any
		if opts.RetainRevisions && !opts.Restore && tableRetainsRevisions(table) {
			previous, _, err = existingImportRow(ctx, db, table, row)
			if err != nil {
				return count, revisions, err
			}
		}
		if !opts.Restore && canonicalImportTable(table) {
			if err := importCanonicalRow(ctx, st, table, row); err != nil {
				return count, revisions, err
			}
		} else {
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
			stmt, err := importInsertStatement(table, cols, quotedCols, holders, opts.Restore)
			if err != nil {
				return count, revisions, err
			}
			if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
				return count, revisions, err
			}
		}
		if previous != nil {
			retained, err := retainChangedRevision(ctx, db, table, row, previous, "snapshot-merge")
			if err != nil {
				return count, revisions, err
			}
			if retained {
				revisions++
			}
		}
	}
	return count, revisions, scanner.Err()
}

func importInsertStatement(table string, cols, quotedCols, holders []string, restore bool) (string, error) {
	base := fmt.Sprintf("insert into %s(%s) values(%s)", quoteIdent(table), strings.Join(quotedCols, ","), strings.Join(holders, ","))
	if restore {
		return base, nil
	}
	keys, ok := importPrimaryKeys[table]
	if !ok {
		return "", fmt.Errorf("missing import key for table %q", table)
	}
	keySet := make(map[string]bool, len(keys))
	quotedKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		keySet[key] = true
		quotedKeys = append(quotedKeys, quoteIdent(key))
	}
	updates := make([]string, 0, len(cols)-len(keys))
	for _, col := range cols {
		if !keySet[col] {
			if table == "record_sources" && col == "payload_json" {
				updates = append(updates, `"payload_json" = case when excluded."alive" = 0 then null else coalesce(excluded."payload_json", "record_sources"."payload_json") end`)
			} else {
				updates = append(updates, quoteIdent(col)+" = excluded."+quoteIdent(col))
			}
		}
	}
	if len(updates) == 0 {
		return base + " on conflict(" + strings.Join(quotedKeys, ",") + ") do nothing", nil
	}
	return base + " on conflict(" + strings.Join(quotedKeys, ",") + ") do update set " + strings.Join(updates, ","), nil
}

func preserveLocalTombstone(ctx context.Context, db *sql.Tx, table string, row map[string]any) (bool, error) {
	recordTable := ""
	switch table {
	case "pages":
		recordTable = "page"
	case "blocks":
		recordTable = "block"
	case "comments":
		recordTable = "comment"
	case "record_sources":
		recordTable, _ = row["record_table"].(string)
	default:
		return false, nil
	}
	recordID, _ := row["id"].(string)
	if table == "record_sources" {
		recordID, _ = row["record_id"].(string)
	}
	source, _ := row["source"].(string)
	if recordTable == "" || recordID == "" || source == "" {
		return false, nil
	}
	var alive int
	err := db.QueryRowContext(ctx, `select alive from record_sources
		where record_table = ? and record_id = ? and source = ?`, recordTable, recordID, source).Scan(&alive)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return alive == 0, nil
}

func tableRetainsRevisions(table string) bool {
	for _, candidate := range revisionTables {
		if table == candidate {
			return true
		}
	}
	return false
}

func retainChangedRevision(ctx context.Context, db *sql.Tx, table string, incoming, previous map[string]any, reason string) (bool, error) {
	current, ok, err := existingImportRow(ctx, db, table, incoming)
	if err != nil || !ok {
		return false, err
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		return false, err
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return false, err
	}
	if string(previousJSON) == string(currentJSON) {
		return false, nil
	}
	key, err := importRecordKey(table, incoming)
	if err != nil {
		return false, err
	}
	if _, err := db.ExecContext(ctx, `insert into record_revisions(
		record_table, record_key, payload_json, recorded_at, event_source, reason)
		values (?, ?, ?, ?, 'share-import', ?)`, table, key, string(previousJSON), store.NowMS(), reason); err != nil {
		return false, err
	}
	return true, nil
}

func canonicalImportTable(table string) bool {
	switch table {
	case "pages", "blocks", "comments":
		return true
	default:
		return false
	}
}

func importCanonicalRow(ctx context.Context, st *store.Store, table string, row map[string]any) error {
	switch table {
	case "pages":
		return st.UpsertPage(ctx, store.Page{
			ID: rowString(row, "id"), SpaceID: rowString(row, "space_id"), ParentID: rowString(row, "parent_id"),
			ParentTable: rowString(row, "parent_table"), CollectionID: rowString(row, "collection_id"), Title: rowString(row, "title"),
			URL: rowString(row, "url"), Icon: rowString(row, "icon"), Cover: rowString(row, "cover"), PropertiesJSON: rowString(row, "properties_json"),
			CreatedTime: rowInt64(row, "created_time"), LastEditedTime: rowInt64(row, "last_edited_time"), Alive: rowBool(row, "alive"),
			Source: rowString(row, "source"), RawJSON: rowString(row, "raw_json"), SyncedAt: rowInt64(row, "synced_at"),
		})
	case "blocks":
		return st.UpsertBlock(ctx, store.Block{
			ID: rowString(row, "id"), PageID: rowString(row, "page_id"), SpaceID: rowString(row, "space_id"), ParentID: rowString(row, "parent_id"),
			ParentTable: rowString(row, "parent_table"), Type: rowString(row, "type"), Text: rowString(row, "text"),
			PropertiesJSON: rowString(row, "properties_json"), ContentJSON: rowString(row, "content_json"), FormatJSON: rowString(row, "format_json"),
			DisplayOrder: rowInt64(row, "display_order"), CreatedTime: rowInt64(row, "created_time"), LastEditedTime: rowInt64(row, "last_edited_time"),
			Alive: rowBool(row, "alive"), Source: rowString(row, "source"), RawJSON: rowString(row, "raw_json"), SyncedAt: rowInt64(row, "synced_at"),
		})
	case "comments":
		return st.UpsertComment(ctx, store.Comment{
			ID: rowString(row, "id"), PageID: rowString(row, "page_id"), SpaceID: rowString(row, "space_id"), ParentID: rowString(row, "parent_id"),
			Text: rowString(row, "text"), CreatedByID: rowString(row, "created_by_id"), CreatedTime: rowInt64(row, "created_time"),
			LastEditedTime: rowInt64(row, "last_edited_time"), Alive: rowBool(row, "alive"), RawJSON: rowString(row, "raw_json"),
			Source: rowString(row, "source"), SyncedAt: rowInt64(row, "synced_at"),
		})
	default:
		return fmt.Errorf("unsupported canonical import table %q", table)
	}
}

func rowString(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

func rowInt64(row map[string]any, key string) int64 {
	switch value := row[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return 0
	}
}

func rowBool(row map[string]any, key string) bool {
	return rowInt64(row, key) != 0
}

func existingImportRow(ctx context.Context, db *sql.Tx, table string, incoming map[string]any) (map[string]any, bool, error) {
	keys, ok := importPrimaryKeys[table]
	if !ok {
		return nil, false, fmt.Errorf("missing import key for table %q", table)
	}
	clauses := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		value, exists := incoming[key]
		if !exists {
			return nil, false, fmt.Errorf("snapshot table %s row missing key %q", table, key)
		}
		clauses = append(clauses, quoteIdent(key)+" = ?")
		args = append(args, value)
	}
	rows, err := db.QueryContext(ctx, "select * from "+quoteIdent(table)+" where "+strings.Join(clauses, " and "), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	row, err := scanImportRow(rows)
	return row, true, err
}

func scanImportRow(rows *sql.Rows) (map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	row := make(map[string]any, len(cols))
	for i, col := range cols {
		row[col] = exportValue(values[i])
	}
	return row, nil
}

func importRecordKey(table string, row map[string]any) (string, error) {
	keys, ok := importPrimaryKeys[table]
	if !ok {
		return "", fmt.Errorf("missing import key for table %q", table)
	}
	key := make(map[string]any, len(keys))
	for _, name := range keys {
		value, exists := row[name]
		if !exists || value == nil || value == "" {
			return "", fmt.Errorf("snapshot table %s row missing key %q", table, name)
		}
		key[name] = value
	}
	body, err := json.Marshal(key)
	return string(body), err
}

func retainAllImportRevisions(ctx context.Context, db *sql.Tx, reason string) (int, error) {
	type revision struct {
		table   string
		key     string
		payload string
	}
	count := 0
	for _, table := range revisionTables {
		rows, err := db.QueryContext(ctx, "select * from "+quoteIdent(table))
		if err != nil {
			return count, err
		}
		var pending []revision
		for rows.Next() {
			row, err := scanImportRow(rows)
			if err != nil {
				rows.Close()
				return count, err
			}
			key, err := importRecordKey(table, row)
			if err != nil {
				rows.Close()
				return count, err
			}
			payload, err := json.Marshal(row)
			if err != nil {
				rows.Close()
				return count, err
			}
			pending = append(pending, revision{table: table, key: key, payload: string(payload)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return count, err
		}
		if err := rows.Close(); err != nil {
			return count, err
		}
		for _, item := range pending {
			if _, err := db.ExecContext(ctx, `insert into record_revisions(
				record_table, record_key, payload_json, recorded_at, event_source, reason)
				values (?, ?, ?, ?, 'share-import', ?)`, item.table, item.key, item.payload, store.NowMS(), reason); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func rebuildRecordSources(ctx context.Context, db sqlExecer) error {
	for _, stmt := range []string{
		`insert or ignore into record_sources(
			record_table, record_id, source, synced_at, alive, deleted_at, deletion_source, deletion_reason)
			select 'page', id, source, synced_at, alive,
				case when alive = 0 then synced_at end,
				case when alive = 0 then source end,
				case when alive = 0 then 'snapshot-legacy-tombstone' end
			from pages`,
		`insert or ignore into record_sources(
			record_table, record_id, source, synced_at, alive, deleted_at, deletion_source, deletion_reason)
			select 'block', id, source, synced_at, alive,
				case when alive = 0 then synced_at end,
				case when alive = 0 then source end,
				case when alive = 0 then 'snapshot-legacy-tombstone' end
			from blocks`,
		`insert or ignore into record_sources(
			record_table, record_id, source, synced_at, alive, deleted_at, deletion_source, deletion_reason)
			select 'comment', id, source, synced_at, alive,
				case when alive = 0 then synced_at end,
				case when alive = 0 then source end,
				case when alive = 0 then 'snapshot-legacy-tombstone' end
			from comments`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func normalizeImportedTombstones(ctx context.Context, db sqlExecer) error {
	_, err := db.ExecContext(ctx, `update record_sources set
		deleted_at = coalesce(deleted_at, synced_at),
		deletion_source = coalesce(deletion_source, source),
		deletion_reason = coalesce(deletion_reason, 'snapshot-legacy-tombstone')
		where alive = 0`)
	return err
}

func reconcileImportedAlive(ctx context.Context, db sqlExecer) error {
	for recordTable, table := range map[string]string{
		"page":    "pages",
		"block":   "blocks",
		"comment": "comments",
	} {
		if _, err := db.ExecContext(ctx, `update `+quoteIdent(table)+` set alive = exists(
			select 1 from record_sources
			where record_table = ? and record_id = `+quoteIdent(table)+`.id and alive = 1
		) where exists(
			select 1 from record_sources
			where record_table = ? and record_id = `+quoteIdent(table)+`.id
		)`, recordTable, recordTable); err != nil {
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
