package share

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/openclaw/notcrawl/internal/notiontext"
)

var createExportFile = func(name string) (io.WriteCloser, error) {
	return os.Create(name)
}

func exportTable(ctx context.Context, db *sql.DB, repoPath, table string) (TableManifest, error) {
	path := filepath.ToSlash(filepath.Join("data", table+".jsonl.gz"))
	full := filepath.Join(repoPath, path)
	out, err := createExportFile(full)
	if err != nil {
		return TableManifest{}, err
	}
	gz := gzip.NewWriter(out)
	count, exportErr := encodeExportRows(ctx, db, table, gz)
	gzErr := gz.Close()
	fileErr := out.Close()
	if exportErr != nil {
		return TableManifest{}, exportErr
	}
	if gzErr != nil {
		return TableManifest{}, gzErr
	}
	if fileErr != nil {
		return TableManifest{}, fileErr
	}
	return TableManifest{Name: table, Path: path, Rows: count}, nil
}

func encodeExportRows(ctx context.Context, db *sql.DB, table string, w io.Writer) (int, error) {
	rows, err := db.QueryContext(ctx, "select * from "+quoteIdent(table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	count := 0
	enc := json.NewEncoder(w)
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		row := map[string]any{}
		for i, col := range cols {
			row[col] = exportValue(values[i])
		}
		fields := map[string]string{}
		for key, value := range row {
			if text, ok := value.(string); ok {
				fields[key] = text
			}
		}
		projected, err := notiontext.ExportFields(fields)
		if err != nil {
			return 0, err
		}
		for key, value := range projected {
			row[key] = value
		}
		if err := enc.Encode(row); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func exportValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return x
	}
}
