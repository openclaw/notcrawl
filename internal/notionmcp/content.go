package notionmcp

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	pageIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}`)
	urlPattern    = regexp.MustCompile(`https?://[^\s<>"']+`)
)

func markdownBlockID(pageID string) string {
	return "notion-mcp:" + pageID
}

func pageIDFromReference(ref string) string {
	match := pageIDPattern.FindString(ref)
	if match == "" {
		return ""
	}
	compact := strings.ReplaceAll(strings.ToLower(match), "-", "")
	if len(compact) != 32 {
		return ""
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
}

func parseTimestamp(raw string) int64 {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func extractEnhancedMarkdown(text string) string {
	content, hasContent := taggedContent(text, "content")
	if !hasContent {
		return strings.Trim(text, "\r\n")
	}
	var sections []string
	if properties, ok := taggedContent(text, "properties"); ok && strings.TrimSpace(properties) != "" {
		var formatted strings.Builder
		formatted.WriteString("## Properties\n\n")
		for _, line := range strings.Split(strings.Trim(properties, "\r\n"), "\n") {
			formatted.WriteString("    ")
			formatted.WriteString(line)
			formatted.WriteByte('\n')
		}
		sections = append(sections, strings.TrimRight(formatted.String(), "\n"))
	}
	if strings.TrimSpace(content) != "" {
		sections = append(sections, strings.Trim(content, "\r\n"))
	}
	return strings.Join(sections, "\n\n")
}

func taggedContent(text, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	end := strings.LastIndex(text, close)
	if start < 0 || end < start {
		return "", false
	}
	return text[start+len(open) : end], true
}

func fetchMetadataJSON(result fetchResult) string {
	metadata := sanitizeJSONURLs(result.Metadata)
	payload := struct {
		Metadata json.RawMessage `json:"metadata,omitempty"`
		Title    string          `json:"title,omitempty"`
		URL      string          `json:"url,omitempty"`
	}{
		Metadata: metadata,
		Title:    result.Title,
		URL:      sanitizeSignedURLs(result.URL),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func sanitizeJSONURLs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	value = sanitizeJSONValue(value)
	sanitized, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return sanitized
}

func sanitizeJSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeSignedURLs(typed)
	case []any:
		for i := range typed {
			typed[i] = sanitizeJSONValue(typed[i])
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = sanitizeJSONValue(item)
		}
		return typed
	default:
		return value
	}
}

func sanitizeSignedURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		candidate := raw
		trailing := ""
		for len(candidate) > 0 && strings.ContainsRune(").,;]}", rune(candidate[len(candidate)-1])) {
			trailing = candidate[len(candidate)-1:] + trailing
			candidate = candidate[:len(candidate)-1]
		}
		parsed, err := url.Parse(strings.ReplaceAll(candidate, "&amp;", "&"))
		if err != nil {
			return raw
		}
		sensitive := false
		for key := range parsed.Query() {
			lower := strings.ToLower(key)
			normalized := strings.ReplaceAll(lower, "-", "_")
			if strings.HasPrefix(lower, "x-amz-") ||
				strings.Contains(lower, "signature") ||
				strings.Contains(lower, "credential") ||
				strings.Contains(lower, "security-token") ||
				normalized == "token" || strings.HasSuffix(normalized, "_token") ||
				normalized == "jwt" || normalized == "api_key" ||
				normalized == "apikey" || normalized == "client_secret" ||
				lower == "sig" || lower == "policy" || lower == "key-pair-id" {
				sensitive = true
				break
			}
		}
		if !sensitive {
			return raw
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String() + trailing
	})
}
