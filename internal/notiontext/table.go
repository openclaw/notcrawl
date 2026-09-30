package notiontext

import "encoding/json"

// TableRowCells reads the official API's cell arrays from archived properties.
func TableRowCells(raw string) ([]string, bool) {
	var properties struct {
		Cells [][]any `json:"cells"`
	}
	if json.Unmarshal([]byte(raw), &properties) != nil || len(properties.Cells) == 0 {
		return nil, false
	}
	cells := make([]string, len(properties.Cells))
	for i, cell := range properties.Cells {
		cells[i] = Plain(cell)
	}
	return cells, true
}
