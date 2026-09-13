package notionapi

import (
	"fmt"
)

func nextListCursor(resp obj, seen map[string]bool, op string) (string, bool, error) {
	if !resp.bool("has_more") {
		return "", false, nil
	}
	cursor, _ := resp["next_cursor"].(string)
	if cursor == "" {
		return "", false, fmt.Errorf("%s has_more without a nonempty string next_cursor", op)
	}
	// Cursors are opaque. Compare exact values without limiting healthy listings.
	if seen[cursor] {
		return "", false, fmt.Errorf("%s repeated cursor", op)
	}
	seen[cursor] = true
	return cursor, true, nil
}
