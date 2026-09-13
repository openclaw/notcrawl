package markdown

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

func renderBlocks(b *strings.Builder, pageID string, blocks []store.Block) {
	children := map[string][]store.Block{}
	for _, block := range blocks {
		if block.ID == pageID {
			continue
		}
		parent := block.ParentID
		children[parent] = append(children[parent], block)
	}
	for parent := range children {
		store.SortBlockSiblings(children[parent])
	}

	renderChildren(b, pageID, children, 0)
	if len(children[pageID]) == 0 {
		var loose []store.Block
		for _, block := range blocks {
			if block.ID != pageID && block.ParentID != pageID {
				loose = append(loose, block)
			}
		}
		for _, block := range loose {
			renderBlock(b, block, 0)
		}
	}
}

func renderChildren(b *strings.Builder, parentID string, children map[string][]store.Block, depth int) {
	for _, block := range children[parentID] {
		renderBlock(b, block, depth)
		renderChildren(b, block.ID, children, depth+1)
	}
}

func renderBlock(b *strings.Builder, block store.Block, depth int) {
	text := notiontext.MarkdownEscape(block.Text)
	indent := strings.Repeat("  ", depth)
	switch block.Type {
	case store.BlockTypeNotionMCPMarkdown:
		text = strings.Trim(block.Text, "\r\n")
		if text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	case "header", "heading_1":
		writeLine(b, "# "+text)
	case "sub_header", "heading_2":
		writeLine(b, "## "+text)
	case "sub_sub_header", "heading_3":
		writeLine(b, "### "+text)
	case "bulleted_list", "bulleted_list_item":
		writeLine(b, indent+"- "+fallback(text, block.Type))
	case "numbered_list", "numbered_list_item":
		writeLine(b, indent+"1. "+fallback(text, block.Type))
	case "to_do", "to_do_item":
		mark := " "
		if todoChecked(block) {
			mark = "x"
		}
		writeLine(b, indent+"- ["+mark+"] "+fallback(text, block.Type))
	case "quote":
		writeLine(b, "> "+fallback(text, block.Type))
	case "code":
		b.WriteString("```text\n")
		b.WriteString(text)
		b.WriteString("\n```\n\n")
	case "divider":
		writeLine(b, "---")
	case "image", "file", "pdf", "video", "figma", "drive":
		writeLine(b, fmt.Sprintf("[%s: %s]", block.Type, fallback(text, block.ID)))
	case "column", "column_list", "table", "table_row", "collection_view":
		if text != "" {
			writeLine(b, text)
		}
	default:
		if text != "" {
			writeLine(b, text)
		} else if block.Type != "" {
			writeLine(b, fmt.Sprintf("[%s]", block.Type))
		}
	}
}

func todoChecked(block store.Block) bool {
	var properties map[string]json.RawMessage
	if json.Unmarshal([]byte(block.PropertiesJSON), &properties) != nil {
		return false
	}
	var checked bool
	if json.Unmarshal(properties["checked"], &checked) == nil {
		return checked
	}
	if block.Source == store.SourceDesktop {
		var value [][]string
		if json.Unmarshal(properties["checked"], &value) == nil && len(value) > 0 && len(value[0]) > 0 {
			return value[0][0] == "Yes"
		}
	}
	return false
}

func writeLine(b *strings.Builder, line string) {
	line = strings.TrimRight(line, " ")
	if line == "" {
		return
	}
	b.WriteString(line)
	b.WriteString("\n\n")
}

func fallback(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}
