package notiontext

// CodeText preserves whitespace in API code blocks, including between segments.
func CodeText(body any) string {
	if m, ok := body.(map[string]any); ok {
		if segments, ok := m["rich_text"].([]any); ok {
			if text, ok := richTextRun(segments); ok {
				return text
			}
		}
	}
	return Plain(body)
}
