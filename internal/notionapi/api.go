package notionapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

const SourceName = "api"

type Client struct {
	BaseURL string
	Version string
	Token   string
	HTTP    *http.Client
	Trace   *slog.Logger
}

type Summary struct {
	Users        int
	Pages        int
	Blocks       int
	Comments     int
	Databases    int
	DatabaseRows int
	Warnings     []string
}

func (c Client) Sync(ctx context.Context, st *store.Store) (Summary, error) {
	if strings.TrimSpace(c.Token) == "" {
		return Summary{}, fmt.Errorf("missing Notion API token")
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.notion.com/v1"
	}
	if c.Version == "" {
		c.Version = "2026-03-11"
	}
	c.HTTP = httpClientOrDefault(c.HTTP)
	var s Summary
	seenPages := map[string]bool{}
	complete := true
	started := time.Now()
	c.tracePhase("users", "started", started)
	users, err := c.listUsers(ctx)
	if err != nil {
		if !isRestrictedResourceError(err) {
			return s, err
		}
		s.Warnings = append(s.Warnings, "Notion API user listing is forbidden; continuing without user labels.")
	} else {
		for _, u := range users {
			raw := notiontext.MarshalRaw(u)
			if err := st.UpsertUser(ctx, store.User{
				ID: u.string("id"), Name: userName(u), Email: userEmail(u), RawJSON: raw, Source: SourceName, SyncedAt: store.NowMS(),
			}); err != nil {
				return s, err
			}
			s.Users++
		}
	}
	c.tracePhase("users", "finished", started, "users", s.Users)
	started = time.Now()
	c.tracePhase("pages", "started", started)
	pages, err := c.searchPages(ctx)
	if err != nil {
		return s, err
	}
	for _, page := range pages {
		count, comments, warnings, err := c.ingestPage(ctx, st, page, ingestPageOptions{FetchBlocks: true, FetchComments: true})
		if err != nil {
			return s, err
		}
		s.Pages++
		seenPages[page.string("id")] = true
		if len(warnings) > 0 {
			complete = false
		}
		s.Blocks += count
		s.Comments += comments
		s.Warnings = append(s.Warnings, warnings...)
		c.tracePhase("pages", "progress", started, "pages", s.Pages, "blocks", s.Blocks, "comments", s.Comments)
	}
	c.tracePhase("pages", "finished", started, "pages", s.Pages, "blocks", s.Blocks, "comments", s.Comments)
	started = time.Now()
	c.tracePhase("collections", "started", started)
	collections, err := c.searchCollections(ctx)
	if err != nil {
		return s, err
	}
	for _, collection := range collections {
		rows, err := c.ingestCollection(ctx, st, collection, seenPages)
		if err != nil {
			return s, err
		}
		s.Databases++
		s.DatabaseRows += rows
		c.tracePhase("collections", "progress", started, "databases", s.Databases, "database_rows", s.DatabaseRows)
	}
	c.tracePhase("collections", "finished", started, "databases", s.Databases, "database_rows", s.DatabaseRows)
	if s.Pages == 0 && s.Databases == 0 && s.Blocks == 0 && s.Comments == 0 {
		complete = false
		status, err := st.Status(ctx)
		if err != nil {
			return s, err
		}
		warning := "Notion API discovery returned zero pages, databases, blocks, and comments; check integration sharing and token scope."
		if status.Pages > 0 {
			warning = fmt.Sprintf("%s Existing local mirror still has %d pages.", warning, status.Pages)
		}
		s.Warnings = append(s.Warnings, warning)
	} else if len(seenPages) == 0 {
		complete = false
		s.Warnings = append(s.Warnings, "Notion API discovery returned databases but no pages or rows; keeping existing API pages.")
	}
	// Only a full, nonempty discovery can retire omitted pages. Keep the
	// source tombstones, fallback content, and search projections atomic.
	if err := writePageBatch(ctx, st, func() error {
		if complete {
			if err := st.RetireSourcePagesNotSeen(ctx, SourceName, seenPages); err != nil {
				return err
			}
		}
		return st.SetSyncState(ctx, SourceName, "workspace", "default", time.Now().Format(time.RFC3339))
	}); err != nil {
		return s, err
	}
	return s, nil
}

func (c Client) listUsers(ctx context.Context) ([]obj, error) {
	var out []obj
	cursor := ""
	seen := map[string]bool{}
	for {
		path := "/users?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}
		var resp obj
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		for _, item := range asSlice(resp["results"]) {
			if m, ok := item.(map[string]any); ok {
				out = append(out, obj(m))
			}
		}
		next, more, err := nextListCursor(resp, seen, "Notion users/list")
		if err != nil {
			return nil, err
		}
		if !more {
			return out, nil
		}
		cursor = next
	}
}

func (c Client) searchPages(ctx context.Context) ([]obj, error) {
	return c.searchObjects(ctx, "page")
}

func (c Client) searchCollections(ctx context.Context) ([]obj, error) {
	return c.searchObjects(ctx, c.collectionSearchType())
}

func (c Client) searchObjects(ctx context.Context, objectType string) ([]obj, error) {
	var out []obj
	cursor := ""
	seen := map[string]bool{}
	for {
		body := obj{"page_size": 100, "filter": obj{"property": "object", "value": objectType}}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var resp obj
		if err := c.do(ctx, http.MethodPost, "/search", body, &resp); err != nil {
			return nil, err
		}
		items, err := discoveryObjects(resp)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if typ := item.string("object"); typ != "" && typ != objectType {
				return nil, fmt.Errorf("Notion search returned an unexpected object type")
			}
			out = append(out, item)
		}
		next, more, err := nextListCursor(resp, seen, "Notion search")
		if err != nil {
			return nil, err
		}
		if !more {
			return out, nil
		}
		cursor = next
	}
}

func discoveryObjects(resp obj) ([]obj, error) {
	// A malformed success response cannot establish complete coverage.
	if _, ok := resp["has_more"].(bool); !ok {
		return nil, fmt.Errorf("Notion discovery requires a boolean has_more")
	}
	results, ok := resp["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("Notion discovery requires a results array")
	}
	items := make([]obj, 0, len(results))
	for _, result := range results {
		item, ok := result.(map[string]any)
		if !ok || strings.TrimSpace(obj(item).string("id")) == "" {
			return nil, fmt.Errorf("Notion discovery requires objects with nonempty IDs")
		}
		items = append(items, obj(item))
	}
	return items, nil
}

type ingestPageOptions struct {
	CollectionID  string
	FetchBlocks   bool
	FetchComments bool
}

func (c Client) ingestPage(ctx context.Context, st *store.Store, page obj, opts ingestPageOptions) (blockCount int, commentCount int, warnings []string, err error) {
	raw := notiontext.MarshalRaw(page)
	props := marshalAny(page["properties"])
	parent := page.mapObj("parent")
	parentID := parent.string("page_id")
	if parentID == "" {
		parentID = parent.string("database_id")
	}
	if parentID == "" {
		parentID = parent.string("data_source_id")
	}
	collectionID := opts.CollectionID
	if collectionID == "" && (parent.string("type") == "database_id" || parent.string("type") == "data_source_id") {
		collectionID = parentID
	}
	spaceID := parent.string("workspace")
	p := store.Page{
		ID:             page.string("id"),
		SpaceID:        spaceID,
		ParentID:       parentID,
		ParentTable:    parent.string("type"),
		CollectionID:   collectionID,
		Title:          titleFromAPIPage(page),
		URL:            page.string("url"),
		PropertiesJSON: props,
		CreatedTime:    parseTimeMS(page.string("created_time")),
		LastEditedTime: parseTimeMS(page.string("last_edited_time")),
		Alive:          !page.bool("archived") && !page.bool("in_trash"),
		Source:         SourceName,
		RawJSON:        raw,
		SyncedAt:       store.NowMS(),
	}
	if p.Title == "" {
		p.Title = "Untitled"
	}
	if err := writePageBatch(ctx, st, func() error {
		if opts.FetchBlocks {
			if err := st.ClearSyncState(ctx, SourceName, "page_blocks", p.ID); err != nil {
				return err
			}
		}
		if err := st.UpsertPage(ctx, p); err != nil {
			return err
		}
		if !p.Alive {
			if _, err := st.RetireSourcePageBlocks(ctx, SourceName, p.ID); err != nil {
				return err
			}
			if _, err := st.RetireSourcePageComments(ctx, SourceName, p.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, 0, nil, err
	}
	if !p.Alive {
		return 0, 0, nil, nil
	}
	var blocks, comments int
	var blockWarnings []string
	if opts.FetchBlocks {
		blocks, blockWarnings, err = c.walkBlocks(ctx, st, p.ID, p.ID, p.SpaceID)
		if err != nil {
			return 0, 0, nil, err
		}
		warnings = append(warnings, blockWarnings...)
	}
	if opts.FetchComments {
		comments, err = c.ingestComments(ctx, st, p.ID, p.SpaceID)
		if err != nil {
			return 0, 0, warnings, err
		}
	}
	return blocks, comments, warnings, nil
}
