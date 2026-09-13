package notionapi

import (
	"encoding/json"
	"time"

	"github.com/openclaw/notcrawl/internal/notiontext"
)

type obj map[string]any

func (o obj) string(key string) string {
	if v, ok := o[key].(string); ok {
		return v
	}
	return ""
}

func (o obj) bool(key string) bool {
	if v, ok := o[key].(bool); ok {
		return v
	}
	return false
}

func (o obj) mapObj(key string) obj {
	if v, ok := o[key].(map[string]any); ok {
		return obj(v)
	}
	return nil
}

func userName(u obj) string {
	if name := u.string("name"); name != "" {
		return name
	}
	person := u.mapObj("person")
	return person.string("email")
}

func userEmail(u obj) string {
	person := u.mapObj("person")
	return person.string("email")
}

func titleFromAPIPage(page obj) string {
	props, ok := page["properties"].(map[string]any)
	if !ok {
		return ""
	}
	for _, prop := range props {
		m, ok := prop.(map[string]any)
		if !ok || m["type"] != "title" {
			continue
		}
		return notiontext.Plain(m["title"])
	}
	return ""
}

func marshalAny(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseTimeMS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
