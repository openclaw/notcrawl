package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/store"
)

func runSQL(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	var parsed notcrawlSQLArgs
	if err := parseKongArgs(&parsed, args, "notcrawl sql", stdout, io.Discard); err != nil {
		return err
	}
	if len(parsed.Query) == 0 {
		return fmt.Errorf("sql query required")
	}
	query := strings.TrimSpace(strings.Join(parsed.Query, " "))
	if !isSQLInspectionQuery(query) {
		return fmt.Errorf("only read-only select/with/pragma queries are allowed")
	}
	st, err := store.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open archive for read-only SQL (run sync first to create or upgrade an archive): %w", err)
	}
	defer st.Close()
	rows, err := st.DB().QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printRows(stdout, rows)
}

type notcrawlSQLArgs struct {
	Query []string `arg:"" optional:"" passthrough:"all" name:"query" help:"Read-only SQL query."`
}

func printSQLUsage(w io.Writer) {
	fmt.Fprint(w, `Usage of sql:
  notcrawl sql QUERY

Runs a read-only SELECT, WITH, or PRAGMA query against the local archive.
`)
}

func printRows(w io.Writer, rows *sql.Rows) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, value := range values {
			if i > 0 {
				fmt.Fprint(w, "\t")
			}
			switch x := value.(type) {
			case nil:
				fmt.Fprint(w, "")
			case []byte:
				fmt.Fprint(w, string(x))
			default:
				fmt.Fprint(w, x)
			}
		}
		fmt.Fprintln(w)
	}
	return rows.Err()
}

func isSQLInspectionQuery(query string) bool {
	// SQLite's mode=ro enforces archive safety. Limit input to one statement so
	// it cannot disable query_only and then attach a writable database.
	started := false
	ended := false
	for i := 0; i < len(query); i++ {
		switch query[i] {
		case 0:
			return false
		case ' ', '\t', '\r', '\n', '\f', '\v':
			continue
		case ';':
			if !started {
				return false
			}
			ended = true
			continue
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				for i < len(query) && query[i] != '\n' {
					i++
				}
				continue
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				end := strings.Index(query[i+2:], "*/")
				if end < 0 {
					return started // SQLite treats an unfinished block comment as EOF.
				}
				i += end + 3
				continue
			}
		}
		if ended {
			return false
		}
		if !started {
			start := i
			for i < len(query) && sqlIdentifierByte(query[i]) {
				i++
			}
			switch strings.ToLower(query[start:i]) {
			case "select", "with", "pragma":
				started = true
			default:
				return false
			}
			i--
			continue
		}
		switch quote := query[i]; quote {
		case '\'', '"', '`', '[':
			closing := quote
			if quote == '[' {
				closing = ']'
			}
			for i++; ; i++ {
				if i >= len(query) {
					return false
				}
				if query[i] != closing {
					continue
				}
				if quote != '[' && i+1 < len(query) && query[i+1] == closing {
					i++
					continue
				}
				break
			}
		}
	}
	return started
}

func sqlIdentifierByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == '$' || b >= 0x80
}
