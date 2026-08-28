package notionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

const SourceName = "api"

const maxAPIAttempts = 4

// defaultHTTPTimeout bounds Notion API requests when Client.HTTP is nil.
// Callers may inject a custom client, including one with no overall timeout.
const defaultHTTPTimeout = 60 * time.Second

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func httpClientOrDefault(client *http.Client) *http.Client {
	if client == nil {
		return defaultHTTPClient()
	}
	return client
}

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
	if err := st.DeferPageFTS(ctx, func() error {
		started := time.Now()
		c.tracePhase("users", "started", started)
		users, err := c.listUsers(ctx)
		if err != nil {
			if !isRestrictedResourceError(err) {
				return err
			}
			s.Warnings = append(s.Warnings, "Notion API user listing is forbidden; continuing without user labels.")
		} else {
			for _, u := range users {
				raw := notiontext.MarshalRaw(u)
				if err := st.UpsertUser(ctx, store.User{
					ID: u.string("id"), Name: userName(u), Email: userEmail(u), RawJSON: raw, Source: SourceName, SyncedAt: store.NowMS(),
				}); err != nil {
					return err
				}
				s.Users++
			}
		}
		c.tracePhase("users", "finished", started, "users", s.Users)
		started = time.Now()
		c.tracePhase("pages", "started", started)
		pages, err := c.searchPages(ctx)
		if err != nil {
			return err
		}
		for _, page := range pages {
			count, comments, warnings, err := c.ingestPage(ctx, st, page, ingestPageOptions{FetchBlocks: true, FetchComments: true})
			if err != nil {
				return err
			}
			s.Pages++
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
			return err
		}
		for _, collection := range collections {
			// Database rows are ingested without FetchBlocks, so
			// ingestCollection can never surface block-children warnings.
			rows, err := c.ingestCollection(ctx, st, collection)
			if err != nil {
				return err
			}
			s.Databases++
			s.DatabaseRows += rows
			c.tracePhase("collections", "progress", started, "databases", s.Databases, "database_rows", s.DatabaseRows)
		}
		c.tracePhase("collections", "finished", started, "databases", s.Databases, "database_rows", s.DatabaseRows)
		if s.Pages == 0 && s.Databases == 0 && s.Blocks == 0 && s.Comments == 0 {
			status, err := st.Status(ctx)
			if err != nil {
				return err
			}
			warning := "Notion API discovery returned zero pages, databases, blocks, and comments; check integration sharing and token scope."
			if status.Pages > 0 {
				warning = fmt.Sprintf("%s Existing local mirror still has %d pages.", warning, status.Pages)
			}
			s.Warnings = append(s.Warnings, warning)
		}
		if err := st.SetSyncState(ctx, SourceName, "workspace", "default", time.Now().Format(time.RFC3339)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return s, err
	}
	return s, nil
}

type obj map[string]any

func (o obj) string(key string) string {
	if v, ok := o[key].(string); ok {
		return v
	}
	return ""
}

func (o obj) bool(key string) bool {
	if v, ok := o[key].(bool); ok {
		return v
	}
	return false
}

func (o obj) mapObj(key string) obj {
	if v, ok := o[key].(map[string]any); ok {
		return obj(v)
	}
	return nil
}

func nextListCursor(resp obj, seen map[string]bool, op string) (string, bool, error) {
	if !truthy(resp["has_more"]) {
		return "", false, nil
	}
	cursor, _ := resp["next_cursor"].(string)
	if cursor == "" {
		return "", false, nil
	}
	// Cursors are opaque. Compare exact values without limiting healthy listings.
	if seen[cursor] {
		return "", false, fmt.Errorf("%s repeated cursor", op)
	}
	seen[cursor] = true
	return cursor, true, nil
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
		for _, item := range asSlice(resp["results"]) {
			if m, ok := item.(map[string]any); ok {
				out = append(out, obj(m))
			}
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
	if opts.FetchBlocks {
		if err := st.ClearSyncState(ctx, SourceName, "page_blocks", p.ID); err != nil {
			return 0, 0, nil, err
		}
	}
	if err := st.UpsertPage(ctx, p); err != nil {
		return 0, 0, nil, err
	}
	if !p.Alive {
		if _, err := st.RetireSourcePageBlocks(ctx, SourceName, p.ID); err != nil {
			return 0, 0, nil, err
		}
		if _, err := st.RetireSourcePageComments(ctx, SourceName, p.ID); err != nil {
			return 0, 0, nil, err
		}
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
		// Only mark this page's block sync complete when every children batch
		// was fetched; a skipped batch (e.g. an unsupported block type) must
		// leave it unmarked so notion-mcp's automatic repair pass (see
		// automaticCandidates in internal/notionmcp/client.go, which treats an
		// unset api/page_blocks "complete" state as a repair candidate) keeps
		// retrying it instead of the gap going untracked.
		if len(blockWarnings) == 0 {
			if err := st.SetSyncState(ctx, SourceName, "page_blocks", p.ID, "complete"); err != nil {
				return 0, 0, nil, err
			}
		}
	}
	if opts.FetchComments {
		comments, err = c.ingestComments(ctx, st, p.ID, p.SpaceID)
		if err != nil {
			return 0, 0, warnings, err
		}
	}
	return blocks, comments, warnings, nil
}

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
			// Row pages are ingested with FetchBlocks/FetchComments both
			// false, so ingestPage never walks blocks here and its warnings
			// return is always empty; nothing to propagate.
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
		if _, err := st.RetireSourcePageBlocksNotSyncedAt(ctx, SourceName, pageID, syncedAt); err != nil {
			return count, warnings, err
		}
	}
	return count, warnings, nil
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
			if err := st.UpsertBlock(ctx, store.Block{
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
			}); err != nil {
				return count, warnings, err
			}
			count++
			if shouldFetchBlockChildren(block) {
				n, childWarnings, err := c.walkBlocksAt(ctx, st, pageID, block.string("id"), spaceID, syncedAt)
				warnings = append(warnings, childWarnings...)
				if err != nil {
					return count, warnings, err
				}
				count += n
			}
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
	if !truthy(block["has_children"]) {
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

func (c Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = b
	}
	for attempt := 1; attempt <= maxAPIAttempts; attempt++ {
		started := time.Now()
		c.traceRequest(path, "started", attempt, 0, started, 0)
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
		if err != nil {
			c.traceRequest(path, "request_error", attempt, 0, started, 0)
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Notion-Version", c.Version)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			c.traceRequest(path, "transport_error", attempt, 0, started, 0)
			if attempt < maxAPIAttempts && shouldRetryTransportError(ctx, method, path, err) {
				c.traceRequest(path, "retry", attempt, 0, started, 0)
				if err := waitBeforeRetry(ctx, 0); err != nil {
					return err
				}
				continue
			}
			return err
		}
		c.traceRequest(path, "received", attempt, resp.StatusCode, started, 0)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			responseBody, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				c.traceRequest(path, "read_error", attempt, resp.StatusCode, started, 0)
				if attempt < maxAPIAttempts && shouldRetryTransportError(ctx, method, path, readErr) {
					c.traceRequest(path, "retry", attempt, resp.StatusCode, started, 0)
					if err := waitBeforeRetry(ctx, 0); err != nil {
						return err
					}
					continue
				}
				return readErr
			}
			err := json.Unmarshal(responseBody, out)
			state := "finished"
			if err != nil {
				state = "decode_error"
			}
			c.traceRequest(path, state, attempt, resp.StatusCode, started, 0)
			return err
		}

		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		c.traceRequest(path, "failed", attempt, resp.StatusCode, started, 0)
		apiErr := apiErrorFromResponse(method, path, resp, b)
		if attempt < maxAPIAttempts && shouldRetry(apiErr) {
			c.traceRequest(path, "retry", attempt, resp.StatusCode, started, apiErr.RetryAfter)
			if err := waitBeforeRetry(ctx, apiErr.RetryAfter); err != nil {
				return err
			}
			continue
		}
		return apiErr
	}
	return nil
}

type notionAPIError struct {
	Method     string
	Path       string
	Status     string
	StatusCode int
	Code       string
	Message    string
	Body       string
	RetryAfter time.Duration
	Retryable  bool
}

func (e notionAPIError) Error() string {
	if e.Code != "" || e.Message != "" {
		return fmt.Sprintf("notion api %s %s: %s: %s: %s", e.Method, e.Path, e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("notion api %s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

func apiErrorFromResponse(method, path string, resp *http.Response, body []byte) notionAPIError {
	bodyText := strings.TrimSpace(string(body))
	apiErr := notionAPIError{
		Method:     method,
		Path:       path,
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Body:       bodyText,
		RetryAfter: retryAfter(resp.Header.Get("Retry-After"), body),
	}
	var payload struct {
		Code       string  `json:"code"`
		Message    string  `json:"message"`
		Retryable  bool    `json:"retryable"`
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		apiErr.Code = payload.Code
		apiErr.Message = payload.Message
		apiErr.Retryable = payload.Retryable
		if payload.RetryAfter > 0 && apiErr.RetryAfter == 0 {
			apiErr.RetryAfter = time.Duration(payload.RetryAfter * float64(time.Second))
		}
	}
	return apiErr
}

func shouldRetry(err notionAPIError) bool {
	if err.StatusCode == http.StatusTooManyRequests || err.Retryable {
		return true
	}
	return err.StatusCode == http.StatusBadGateway ||
		err.StatusCode == http.StatusServiceUnavailable ||
		err.StatusCode == http.StatusGatewayTimeout ||
		err.StatusCode == 524 // Cloudflare timeout, returned by Notion for transient upstream stalls.
}

func shouldRetryTransportError(ctx context.Context, method, path string, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	return isReplaySafeRequest(method, path) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func isReplaySafeRequest(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	path, _, _ = strings.Cut(path, "?")
	if path == "/search" {
		return true
	}
	return (strings.HasPrefix(path, "/databases/") || strings.HasPrefix(path, "/data_sources/")) &&
		strings.HasSuffix(path, "/query")
}

func retryAfter(header string, body []byte) time.Duration {
	if header != "" {
		if seconds, err := time.ParseDuration(header + "s"); err == nil && seconds > 0 {
			return seconds
		}
		if when, err := http.ParseTime(header); err == nil {
			if wait := time.Until(when); wait > 0 {
				return wait
			}
		}
	}
	var payload struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.RetryAfter > 0 {
		return time.Duration(payload.RetryAfter * float64(time.Second))
	}
	return 0
}

func waitBeforeRetry(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isIgnoredCommentError(err error) bool {
	apiErr, ok := err.(notionAPIError)
	if !ok {
		return false
	}
	if apiErr.StatusCode == http.StatusNotFound || apiErr.Code == "not_found" {
		return true
	}
	return isRestrictedResourceError(err)
}

func isRestrictedResourceError(err error) bool {
	apiErr, ok := err.(notionAPIError)
	return ok && apiErr.StatusCode == http.StatusForbidden && apiErr.Code == "restricted_resource"
}

// isUnsupportedBlockChildrenError reports whether err is Notion rejecting a
// block-children listing because the batch contains a block type integrations
// cannot receive (e.g. ai_block). Notion returns this as a 400 validation_error
// rather than a per-block signal, so the whole children listing must be skipped.
func isUnsupportedBlockChildrenError(err error) bool {
	apiErr, ok := err.(notionAPIError)
	if !ok {
		return false
	}
	return apiErr.StatusCode == http.StatusBadRequest &&
		apiErr.Code == "validation_error" &&
		strings.Contains(apiErr.Message, "not supported via the API")
}

func userName(u obj) string {
	if name := u.string("name"); name != "" {
		return name
	}
	person := u.mapObj("person")
	return person.string("email")
}

func userEmail(u obj) string {
	person := u.mapObj("person")
	return person.string("email")
}

func titleFromAPIPage(page obj) string {
	props, ok := page["properties"].(map[string]any)
	if !ok {
		return ""
	}
	for _, prop := range props {
		m, ok := prop.(map[string]any)
		if !ok || m["type"] != "title" {
			continue
		}
		return notiontext.Plain(m["title"])
	}
	return ""
}

func marshalAny(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseTimeMS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
