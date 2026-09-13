package share

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/openclaw/notcrawl/internal/store"
)

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

func tableRetainsRevisions(table string) bool {
	return slices.Contains(revisionTables, table)
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
