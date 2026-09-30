package markdown

import (
	"encoding/json"
	"strings"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

func renderTable(b *strings.Builder, table store.Block, children map[string][]store.Block, indent string) bool {
	var properties struct {
		Width        int  `json:"table_width"`
		ColumnHeader bool `json:"has_column_header"`
	}
	if json.Unmarshal([]byte(table.PropertiesJSON), &properties) != nil || properties.Width <= 0 {
		return false
	}
	var rows [][]string
	for _, row := range children[table.ID] {
		cells, ok := notiontext.TableRowCells(row.PropertiesJSON)
		if row.Type != "table_row" || !ok || len(cells) != properties.Width || len(children[row.ID]) != 0 {
			return false
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return false
	}
	// GFM requires a header; an empty one preserves the first data row in tables without one.
	header := make([]string, properties.Width)
	if properties.ColumnHeader {
		header, rows = rows[0], rows[1:]
	}
	writeTableRow(b, header, indent)
	b.WriteString(indent + "|" + strings.Repeat(" --- |", properties.Width) + "\n")
	for _, row := range rows {
		writeTableRow(b, row, indent)
	}
	writePrefixed(b, "\n", indent)
	return true
}

var tableCellEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "|", "\\|", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "~", "\\~", "#", "\\#", "+", "\\+", "-", "\\-", ".", "\\.", ")", "\\)", "=", "\\=")

func writeTableRow(b *strings.Builder, cells []string, indent string) {
	b.WriteString(indent + "|")
	for _, cell := range cells {
		b.WriteString(" " + tableCellEscaper.Replace(cell) + " |")
	}
	b.WriteString("\n")
}
