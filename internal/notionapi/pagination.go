package notionapi

import (
	"fmt"
	"strings"
)

func listObjects(resp obj, op string) ([]obj, error) {
	// Validate the whole batch before writing it: malformed success responses
	// cannot establish the coverage needed to retire unseen records.
	if _, ok := resp["has_more"].(bool); !ok {
		return nil, fmt.Errorf("%s requires a boolean has_more", op)
	}
	results, ok := resp["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("%s requires a results array", op)
	}
	items := make([]obj, 0, len(results))
	for _, result := range results {
		item, ok := result.(map[string]any)
		if !ok || strings.TrimSpace(obj(item).string("id")) == "" {
			return nil, fmt.Errorf("%s requires objects with nonempty IDs", op)
		}
		items = append(items, obj(item))
	}
	return items, nil
}

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
