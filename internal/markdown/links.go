package markdown

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

func renderLinkedBlock(b *strings.Builder, block store.Block) bool {
	target, caption := linkedBlockContent(block)
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return false
	}
	text := block.Text
	if strings.TrimSpace(text) == "" {
		text = caption
	}
	if strings.TrimSpace(text) != target {
		writeLine(b, notiontext.MarkdownEscape(text))
	}
	// Angle brackets delimit Markdown autolinks, including inside URL fragments.
	target = strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20", "\\", "%5C").Replace(u.String())
	writeLine(b, "<"+target+">")
	return true
}

func linkedBlockContent(block store.Block) (string, string) {
	switch block.Type {
	case "bookmark", "embed", "link_preview", "tweet", "video":
	default:
		return "", ""
	}
	var properties struct {
		URL      string `json:"url"`
		Source   any    `json:"source"`
		Caption  any    `json:"caption"`
		External struct {
			URL string `json:"url"`
		} `json:"external"`
		File struct {
			URL string `json:"url"`
		} `json:"file"`
	}
	if json.Unmarshal([]byte(block.PropertiesJSON), &properties) != nil {
		return "", ""
	}
	target := properties.URL
	if block.Source == store.SourceDesktop {
		target = notiontext.Plain(properties.Source)
	} else if target == "" {
		target = properties.External.URL
		if target == "" {
			target = properties.File.URL
		}
	}
	return target, notiontext.Plain(properties.Caption)
}
