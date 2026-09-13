package notionapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

func (c Client) walkBlocks(ctx context.Context, st *store.Store, pageID, parentID, spaceID string) (int, []string, error) {
	syncedAt, err := st.NextSourceSyncAt(ctx, "block", SourceName)
	if err != nil {
		return 0, nil, err
	}
	count, warnings, err := c.walkBlocksAt(ctx, st, pageID, parentID, spaceID, syncedAt)
	if err != nil {
		return count, warnings, err
	}
	// A partial walk (some children batch was skipped, e.g. an unsupported
	// block type) must not retire blocks this run never got a chance to
	// re-fetch — otherwise a page that previously synced cleanly loses its
	// existing content the moment it gains one unfetchable block.
	if len(warnings) == 0 {
		if err := writePageBatch(ctx, st, func() error {
			if _, err := st.RetireSourcePageBlocksNotSyncedAt(ctx, SourceName, pageID, syncedAt); err != nil {
				return err
			}
			return st.SetSyncState(ctx, SourceName, "page_blocks", pageID, "complete")
		}); err != nil {
			return count, warnings, err
		}
	}
	return count, warnings, nil
}

// Each committed API batch includes its search projection; network requests
// and recursive traversal must remain outside this transaction.
func writePageBatch(ctx context.Context, st *store.Store, write func() error) error {
	return st.WithTransaction(ctx, func() error {
		return st.DeferPageFTS(ctx, write)
	})
}

func (c Client) walkBlocksAt(ctx context.Context, st *store.Store, pageID, parentID, spaceID string, syncedAt int64) (int, []string, error) {
	var count int
	var warnings []string
	cursor := ""
	seen := map[string]bool{}
	var displayOrder int64
	for {
		path := fmt.Sprintf("/blocks/%s/children?page_size=100", url.PathEscape(parentID))
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}
		var resp obj
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			if isUnsupportedBlockChildrenError(err) {
				warnings = append(warnings, fmt.Sprintf(
					"Skipped children of block %s on page %s: %v", parentID, pageID, err))
				return count, warnings, nil
			}
			return count, warnings, err
		}
		var batch []store.Block
		var children []string
		for _, item := range asSlice(resp["results"]) {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			block := obj(m)
			typ := block.string("type")
			typeBody := block[typ]
			text := notiontext.Plain(typeBody)
			raw := notiontext.MarshalRaw(block)
			displayOrder++
			batch = append(batch, store.Block{
				ID:             block.string("id"),
				PageID:         pageID,
				SpaceID:        spaceID,
				ParentID:       parentID,
				ParentTable:    "block",
				Type:           typ,
				Text:           text,
				PropertiesJSON: marshalAny(typeBody),
				DisplayOrder:   displayOrder,
				CreatedTime:    parseTimeMS(block.string("created_time")),
				LastEditedTime: parseTimeMS(block.string("last_edited_time")),
				Alive:          !block.bool("archived") && !block.bool("in_trash"),
				Source:         SourceName,
				RawJSON:        raw,
				SyncedAt:       syncedAt,
			})
			if shouldFetchBlockChildren(block) {
				children = append(children, block.string("id"))
			}
		}
		if err := writePageBatch(ctx, st, func() error {
			for _, block := range batch {
				if err := st.UpsertBlock(ctx, block); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, warnings, err
		}
		count += len(batch)
		for _, childID := range children {
			n, childWarnings, err := c.walkBlocksAt(ctx, st, pageID, childID, spaceID, syncedAt)
			warnings = append(warnings, childWarnings...)
			if err != nil {
				return count, warnings, err
			}
			count += n
		}
		next, more, err := nextListCursor(resp, seen, "Notion block children")
		if err != nil {
			return count, warnings, err
		}
		if !more {
			return count, warnings, nil
		}
		cursor = next
	}
}

func shouldFetchBlockChildren(block obj) bool {
	if !block.bool("has_children") {
		return false
	}
	return !isSyncedBlockCopy(block)
}

func isSyncedBlockCopy(block obj) bool {
	if block.string("type") != "synced_block" {
		return false
	}
	body := block.mapObj("synced_block")
	if len(body) == 0 {
		return false
	}
	return len(body.mapObj("synced_from")) > 0
}

func (c Client) ingestComments(ctx context.Context, st *store.Store, pageID, spaceID string) (int, error) {
	var count int
	cursor := ""
	seen := map[string]bool{}
	for {
		path := "/comments?block_id=" + url.QueryEscape(pageID) + "&page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}
		var resp obj
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			if isIgnoredCommentError(err) {
				return count, nil
			}
			return count, err
		}
		for _, item := range asSlice(resp["results"]) {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			comment := obj(m)
			createdBy := comment.mapObj("created_by")
			if err := st.UpsertComment(ctx, store.Comment{
				ID:             comment.string("id"),
				PageID:         pageID,
				SpaceID:        spaceID,
				ParentID:       pageID,
				Text:           notiontext.Plain(comment["rich_text"]),
				CreatedByID:    createdBy.string("id"),
				CreatedTime:    parseTimeMS(comment.string("created_time")),
				LastEditedTime: parseTimeMS(comment.string("last_edited_time")),
				Alive:          true,
				RawJSON:        notiontext.MarshalRaw(comment),
				Source:         SourceName,
				SyncedAt:       store.NowMS(),
			}); err != nil {
				return count, err
			}
			count++
		}
		next, more, err := nextListCursor(resp, seen, "Notion comments/list")
		if err != nil {
			return count, err
		}
		if !more {
			return count, nil
		}
		cursor = next
	}
}
