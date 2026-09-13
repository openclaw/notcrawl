package store

import (
	"cmp"
	"slices"
	"strings"
)

func PreferredPageContentBlocks(blocks []Block, apiBlocksSynced bool) []Block {
	hasNotionMCP := false
	for _, block := range blocks {
		if block.Type == BlockTypeNotionMCPMarkdown {
			hasNotionMCP = true
			break
		}
	}
	if !hasNotionMCP {
		return blocks
	}
	if apiBlocksSynced {
		out := make([]Block, 0, len(blocks))
		for _, candidate := range blocks {
			if candidate.Type != BlockTypeNotionMCPMarkdown {
				out = append(out, candidate)
			}
		}
		return out
	}
	var out []Block
	for _, block := range blocks {
		if block.Type == BlockTypeNotionMCPMarkdown {
			out = append(out, block)
		}
	}
	return out
}

func SortBlockSiblings(blocks []Block) {
	slices.SortStableFunc(blocks, func(a, b Block) int {
		if order := cmp.Compare(a.DisplayOrder, b.DisplayOrder); order != 0 {
			return order
		}
		if created := cmp.Compare(a.CreatedTime, b.CreatedTime); created != 0 {
			return created
		}
		return strings.Compare(a.ID, b.ID)
	})
}
