package notiontext

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	spaceRE                    = regexp.MustCompile(`\s+`)
	legacyInlineLinkArtifactRE = regexp.MustCompile(`\ba\s+((?:https?://|/)[^\s]+)`)
	legacyInlineMarkArtifactRE = regexp.MustCompile(`\s+\b[bius]\b($|[\s,.;:])`)
	legacyMentionArtifactRE    = regexp.MustCompile(`\bm\s+[0-9a-fA-F]{8}-[0-9a-fA-F-]{8,}(?:\s+[0-9a-fA-F-]{12,})?`)
	legacyPageMentionRE        = regexp.MustCompile(`(?:‣\s*)?p\s+[0-9a-fA-F]{8}-[0-9a-fA-F-]{8,}(?:\s+[0-9a-fA-F-]{12,})?`)
	legacyLinkedMentionRE      = regexp.MustCompile(`‣\s+lm\s+`)
	legacyBareMentionRE        = regexp.MustCompile(`‣\s+[0-9a-fA-F]{8}-[0-9a-fA-F-]{8,}`)
	spaceBeforePunctuationRE   = regexp.MustCompile(`\s+([,.;:])`)
	repeatedCommaRE            = regexp.MustCompile(`(?:,\s*){2,}`)
	repeatedLinkedPageRE       = regexp.MustCompile(`linked page\b(?:,\s*linked page\b)+`)
)

func Normalize(s string) string {
	return strings.TrimSpace(spaceRE.ReplaceAllString(s, " "))
}

func CleanLegacyArtifacts(s string) string {
	s = legacyInlineLinkArtifactRE.ReplaceAllString(s, "<$1>")
	s = legacyInlineMarkArtifactRE.ReplaceAllString(s, "$1")
	s = legacyMentionArtifactRE.ReplaceAllString(s, "@mention")
	s = legacyPageMentionRE.ReplaceAllString(s, "linked page")
	s = legacyLinkedMentionRE.ReplaceAllString(s, "‣ ")
	s = legacyBareMentionRE.ReplaceAllString(s, "@mention")
	s = Normalize(s)
	s = repeatedCommaRE.ReplaceAllString(s, ", ")
	s = repeatedLinkedPageRE.ReplaceAllString(s, "linked pages")
	s = strings.ReplaceAll(s, "linked pagess", "linked pages")
	s = spaceBeforePunctuationRE.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, " and, ", ", ")
	return Normalize(s)
}

func PlainFromJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return Normalize(raw)
	}
	return Plain(v)
}

func Plain(v any) string {
	var parts []string
	walk(v, &parts)
	return Normalize(strings.Join(parts, " "))
}

func TitleFromProperties(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return ""
	}
	if m, ok := v.(map[string]any); ok {
		for _, key := range []string{"title", "Name", "name"} {
			if text := Plain(m[key]); text != "" {
				return text
			}
		}
		for _, key := range slices.Sorted(maps.Keys(m)) {
			if text := Plain(m[key]); text != "" {
				return text
			}
		}
	}
	return Plain(v)
}

func MarkdownEscape(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, " \n")
}

func ShortID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) > 16 {
		return clean[:8] + "-" + clean[len(clean)-8:]
	}
	if clean == "" {
		return "unknown"
	}
	return clean
}

func Slug(s string) string {
	s = strings.ToLower(Normalize(s))
	var b strings.Builder
	lastDash := false
	wrote := false
	for _, r := range s {
		switch {
		case isSlugRune(r):
			b.WriteRune(r)
			lastDash = false
			wrote = true
		case isSlugSeparator(r):
			if wrote && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		case unicode.IsControl(r):
			continue
		default:
			if wrote && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

func isSlugRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

func isSlugSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(`-_/.\:;`, r)
}

func MarshalRaw(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprintf(`{"marshal_error":%q}`, err.Error())
	}
	return string(b)
}

func walk(v any, parts *[]string) {
	switch x := v.(type) {
	case nil:
		return
	case string:
		if x != "" {
			*parts = append(*parts, x)
		}
	case []any:
		if text, ok := legacyRichTextPart(x); ok {
			*parts = append(*parts, text)
			return
		}
		for _, item := range x {
			walk(item, parts)
		}
	case map[string]any:
		if text, ok := normalizedString(x["plain_text"]); ok {
			*parts = append(*parts, text)
			return
		}
		if text, ok := richTextContent(x["text"]); ok {
			*parts = append(*parts, text)
			return
		}
		if text, ok := normalizedString(x["content"]); ok {
			*parts = append(*parts, text)
			return
		}
		for _, key := range []string{"name", "title", "rich_text", "text"} {
			if value, ok := x[key]; ok {
				walk(value, parts)
			}
		}
	}
}

func legacyRichTextPart(values []any) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	text, ok := normalizedString(values[0])
	if !ok {
		return "", false
	}
	if len(values) < 2 {
		return text, true
	}
	if link := legacyAnnotationLink(values[1]); link != "" {
		return Normalize(text + " <" + link + ">"), true
	}
	return text, true
}

func legacyAnnotationLink(value any) string {
	values, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, item := range values {
		annotation, ok := item.([]any)
		if !ok || len(annotation) < 2 {
			continue
		}
		code, ok := annotation[0].(string)
		if !ok || code != "a" {
			continue
		}
		if link, ok := normalizedString(annotation[1]); ok {
			return link
		}
	}
	return ""
}

func richTextContent(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	return normalizedString(m["content"])
}

func normalizedString(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = Normalize(s)
	if s == "" {
		return "", false
	}
	return s, true
}
