package tableexport

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/openclaw/notcrawl/internal/notiontext"
)

func decodeMap(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func PropertyText(v any, refs ReferenceLabels) string {
	if text, ok := desktopValueText(v, refs); ok {
		return text
	}
	m, ok := v.(map[string]any)
	if !ok {
		return notiontext.Plain(v)
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		return notiontext.Plain(v)
	}
	switch typ {
	case "title", "rich_text":
		return notiontext.Plain(m[typ])
	case "number":
		return numberText(m["number"])
	case "select", "status":
		return namedObject(m[typ])
	case "multi_select":
		return joinNamed(m[typ])
	case "date":
		return dateText(m["date"])
	case "checkbox":
		if b, ok := m["checkbox"].(bool); ok {
			return strconv.FormatBool(b)
		}
	case "url", "email", "phone_number", "created_time", "last_edited_time":
		if s, ok := m[typ].(string); ok {
			return s
		}
	case "people", "files":
		return joinNamed(m[typ])
	case "relation":
		return joinIDs(m[typ], refs)
	case "formula":
		return formulaText(m["formula"], refs)
	case "rollup":
		return rollupText(m["rollup"], refs)
	case "created_by", "last_edited_by":
		return namedObject(m[typ])
	case "unique_id":
		return uniqueIDText(m["unique_id"])
	}
	return notiontext.Plain(v)
}

func desktopValueText(v any, refs ReferenceLabels) (string, bool) {
	text, ok := desktopPlain(v, refs)
	if !ok {
		return "", false
	}
	text = notiontext.Normalize(strings.ReplaceAll(text, " , ", ", "))
	return text, true
}

func desktopPlain(v any, refs ReferenceLabels) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		if x == "‣" {
			return "", true
		}
		return x, true
	case []any:
		if len(x) == 0 {
			return "", true
		}
		if marker, ok := x[0].(string); ok {
			if marker == "‣" && len(x) > 1 {
				return desktopRefListText(x[1], refs), true
			}
			if marker == "," {
				return ",", true
			}
			if marker != "" {
				return marker, true
			}
		}
		parts := make([]string, 0, len(x))
		handled := false
		for _, item := range x {
			text, ok := desktopPlain(item, refs)
			if !ok {
				return "", false
			}
			handled = true
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " "), handled
	default:
		return "", false
	}
}

func desktopRefListText(v any, refs ReferenceLabels) string {
	items, ok := v.([]any)
	if !ok {
		return notiontext.Plain(v)
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := desktopRefText(item, refs); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func desktopRefText(v any, refs ReferenceLabels) string {
	item, ok := v.([]any)
	if !ok || len(item) == 0 {
		return notiontext.Plain(v)
	}
	typ, _ := item[0].(string)
	switch typ {
	case ",":
		return ","
	case "u":
		if id, ok := stringAt(item, 1); ok {
			return labelOrID(refs.Users, id)
		}
	case "p":
		if id, ok := stringAt(item, 1); ok {
			return labelOrID(refs.Pages, id)
		}
	case "d":
		if len(item) > 1 {
			return dateText(item[1])
		}
	}
	return notiontext.Plain(v)
}

func stringAt(items []any, index int) (string, bool) {
	if index >= len(items) {
		return "", false
	}
	s, ok := items[index].(string)
	return s, ok
}

func labelOrID(labels map[string]string, id string) string {
	if label := labels[id]; label != "" {
		return label
	}
	return id
}

func namedObject(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if name, ok := m["name"].(string); ok {
		return name
	}
	if value, ok := m["value"].(string); ok {
		return value
	}
	if id, ok := m["id"].(string); ok {
		return id
	}
	return notiontext.Plain(v)
}

func joinNamed(v any) string {
	items, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := namedObject(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ", ")
}

func joinIDs(v any, refs ReferenceLabels) string {
	items, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := m["id"].(string); ok {
			parts = append(parts, labelOrID(refs.Pages, id))
		}
	}
	return strings.Join(parts, ", ")
}

func dateText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	start, _ := m["start"].(string)
	if start == "" {
		start, _ = m["start_date"].(string)
	}
	end, _ := m["end"].(string)
	if end == "" {
		end, _ = m["end_date"].(string)
	}
	if end != "" {
		return start + "/" + end
	}
	return start
}

func formulaText(v any, refs ReferenceLabels) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "string":
		s, _ := m["string"].(string)
		return s
	case "number":
		return numberText(m["number"])
	case "boolean":
		if b, ok := m["boolean"].(bool); ok {
			return strconv.FormatBool(b)
		}
	case "date":
		return dateText(m["date"])
	}
	if text, ok := desktopValueText(v, refs); ok {
		return text
	}
	return notiontext.Plain(v)
}

func rollupText(v any, refs ReferenceLabels) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "number":
		return numberText(m["number"])
	case "date":
		return dateText(m["date"])
	case "array":
		items, _ := m["array"].([]any)
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text := PropertyText(item, refs); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	}
	if text, ok := desktopValueText(v, refs); ok {
		return text
	}
	return notiontext.Plain(v)
}

func uniqueIDText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	prefix, _ := m["prefix"].(string)
	number := numberText(m["number"])
	return prefix + number
}

func numberText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}
