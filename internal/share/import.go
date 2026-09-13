package share

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openclaw/notcrawl/internal/store"
)

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
		row, err := decodeImportRow(scanner.Bytes())
		if err != nil {
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

func decodeImportRow(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("snapshot row must contain exactly one JSON value")
	}
	// Normalize before keys, revisions, canonical upserts or direct SQL see
	// the row: json.Number must not become a string binding or a zero value.
	for col, value := range row {
		number, ok := value.(json.Number)
		if !ok {
			continue
		}
		if strings.ContainsAny(string(number), ".eE") {
			value, err := number.Float64()
			if err != nil {
				return nil, errors.New("snapshot row contains an unrepresentable decimal")
			}
			row[col] = value
		} else {
			value, err := number.Int64()
			if err != nil {
				return nil, errors.New("snapshot row contains an out-of-range integer")
			}
			row[col] = value
		}
	}
	return row, nil
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

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
