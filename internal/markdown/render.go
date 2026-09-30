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

	renderChildren(b, pageID, children, "")
	if len(children[pageID]) == 0 {
		var loose []store.Block
		for _, block := range blocks {
			if block.ID != pageID && block.ParentID != pageID {
				loose = append(loose, block)
			}
		}
		for _, block := range loose {
			renderBlock(b, block, "")
		}
	}
}

func renderChildren(b *strings.Builder, parentID string, children map[string][]store.Block, indent string) {
	for _, block := range children[parentID] {
		if block.Type == "table" && renderTable(b, block, children, indent) {
			continue
		}
		renderBlock(b, block, indent)
		childIndent := indent
		switch block.Type {
		case "bulleted_list", "bulleted_list_item", "to_do", "to_do_item":
			childIndent += "  "
		case "numbered_list", "numbered_list_item":
			childIndent += "   "
		case "quote":
			childIndent += "> "
		}
		renderChildren(b, block.ID, children, childIndent)
	}
}

func renderBlock(b *strings.Builder, block store.Block, indent string) {
	if block.Type == "quote" {
		writePrefixed(b, fallback(notiontext.MarkdownEscape(block.Text), block.Type)+"\n\n", indent+"> ")
		return
	}
	var content strings.Builder
	renderBlockContent(&content, block)
	writePrefixed(b, content.String(), indent)
}

func writePrefixed(b *strings.Builder, text, prefix string) {
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if line == "\n" {
			b.WriteString(strings.TrimRight(prefix, " "))
		} else {
			b.WriteString(prefix)
		}
		b.WriteString(line)
	}
}

func renderBlockContent(b *strings.Builder, block store.Block) {
	text := notiontext.MarkdownEscape(block.Text)
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
		writeLine(b, "- "+fallback(text, block.Type))
	case "numbered_list", "numbered_list_item":
		writeLine(b, "1. "+fallback(text, block.Type))
	case "to_do", "to_do_item":
		mark := " "
		if todoChecked(block) {
			mark = "x"
		}
		writeLine(b, "- ["+mark+"] "+fallback(text, block.Type))
	case "code":
		b.WriteString("```text\n")
		b.WriteString(text)
		b.WriteString("\n```\n\n")
	case "divider":
		writeLine(b, "---")
	case "image", "file", "pdf", "video", "figma", "drive":
		writeLine(b, fmt.Sprintf("[%s: %s]", block.Type, fallback(text, block.ID)))
	case "table_row":
		if cells, ok := notiontext.TableRowCells(block.PropertiesJSON); ok {
			for i, cell := range cells {
				cells[i] = tableCellEscaper.Replace(cell)
			}
			text = strings.Join(cells, " | ")
		}
		writeLine(b, fallback(text, "[table_row]"))
	case "column", "column_list", "collection_view":
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
