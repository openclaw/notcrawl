package notionapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

func (c Client) ingestCollection(ctx context.Context, st *store.Store, collection obj) (int, error) {
	id := collection.string("id")
	raw := notiontext.MarshalRaw(collection)
	parent := collection.mapObj("parent")
	if len(parent) == 0 {
		parent = collection.mapObj("database_parent")
	}
	parentID := firstNonEmpty(parent.string("database_id"), parent.string("page_id"), parent.string("block_id"), parent.string("workspace"))
	name := notiontext.Plain(collection["title"])
	if name == "" {
		name = id
	}
	if err := st.UpsertCollection(ctx, store.Collection{
		ID:          id,
		SpaceID:     parent.string("workspace"),
		ParentID:    parentID,
		ParentTable: parent.string("type"),
		Name:        name,
		SchemaJSON:  marshalAny(collection["properties"]),
		FormatJSON:  marshalAny(collection),
		RawJSON:     raw,
		Source:      SourceName,
		SyncedAt:    store.NowMS(),
	}); err != nil {
		return 0, err
	}
	if err := st.UpsertRawRecord(ctx, store.RawRecord{
		Source: SourceName, RecordTable: c.collectionSearchType(), RecordID: id, ParentID: parentID,
		SpaceID: parent.string("workspace"), RawJSON: raw, SyncedAt: store.NowMS(),
	}); err != nil {
		return 0, err
	}
	return c.queryCollection(ctx, st, id)
}

func (c Client) queryCollection(ctx context.Context, st *store.Store, collectionID string) (int, error) {
	var count int
	cursor := ""
	seen := map[string]bool{}
	for {
		body := obj{"page_size": 100}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var resp obj
		path := fmt.Sprintf("%s/%s/query", c.collectionQueryBasePath(), url.PathEscape(collectionID))
		if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
			return count, err
		}
		for _, item := range asSlice(resp["results"]) {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if itemType := obj(m).string("object"); itemType != "" && itemType != "page" {
				if itemType == c.collectionSearchType() {
					if _, err := c.ingestCollection(ctx, st, obj(m)); err != nil {
						return count, err
					}
				}
				continue
			}
			// Collection queries refresh row metadata without fetching page bodies.
			if _, _, _, err := c.ingestPage(ctx, st, obj(m), ingestPageOptions{CollectionID: collectionID}); err != nil {
				return count, err
			}
			count++
		}
		next, more, err := nextListCursor(resp, seen, "Notion collection query")
		if err != nil {
			return count, err
		}
		if !more {
			return count, nil
		}
		cursor = next
	}
}

func (c Client) collectionSearchType() string {
	if c.usesDataSourceAPI() {
		return "data_source"
	}
	return "database"
}

func (c Client) collectionQueryBasePath() string {
	if c.usesDataSourceAPI() {
		return "/data_sources"
	}
	return "/databases"
}

func (c Client) usesDataSourceAPI() bool {
	return c.Version >= "2025-09-03"
}
