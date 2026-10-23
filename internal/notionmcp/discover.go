package notionmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/notcrawl/internal/store"
)

const (
	discoveryEntity      = "discovery"
	discoveryLastRunKey  = "last_run"
	defaultSweepPageSize = 25
	firstSweepWindow     = 7 * 24 * time.Hour
	sweepOverlap         = time.Hour
)

var collectionRefPattern = regexp.MustCompile(`(?i)collection://([0-9a-f-]{32,36})`)

type crawlQueue struct {
	items []pageCandidate
	index map[string]int
	head  int
}

func newCrawlQueue() *crawlQueue {
	return &crawlQueue{index: map[string]int{}}
}

func (q *crawlQueue) add(candidate pageCandidate) {
	if candidate.ID == "" {
		return
	}
	if i, ok := q.index[candidate.ID]; ok {
		if candidate.Refresh {
			q.items[i].Refresh = true
		}
		return
	}
	q.index[candidate.ID] = len(q.items)
	q.items = append(q.items, candidate)
}

func (q *crawlQueue) remaining() int {
	return len(q.items) - q.head
}

func (q *crawlQueue) pop() pageCandidate {
	candidate := q.items[q.head]
	q.head++
	return candidate
}

func (c Client) discover(
	ctx context.Context,
	gateway *gatewayClient,
	tools notionToolset,
	st *store.Store,
	existing map[string]store.Page,
	opts SyncOptions,
) (Summary, error) {
	startedAt := time.Now().UTC()
	var summary Summary
	queue := newCrawlQueue()
	since, err := discoverySince(ctx, st, startedAt)
	if err != nil {
		return summary, err
	}
	sweepRecentEdits(ctx, gateway, tools, since, existing, queue, &summary)
	for _, ref := range opts.PageIDs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		id := pageIDFromReference(ref)
		if id == "" {
			return summary, fmt.Errorf("could not determine Notion page ID from %q", ref)
		}
		queue.add(pageCandidate{ID: id, FetchRef: ref, Page: existing[id], Refresh: true})
	}
	for _, query := range opts.Queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		results, err := gateway.searchPages(ctx, tools.Search, query, defaultSweepPageSize, nil)
		if err != nil {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("Notion MCP search failed for %q: %v", query, err))
			continue
		}
		for _, result := range results {
			candidate := searchCandidate(result, existing)
			candidate.Refresh = true
			queue.add(candidate)
		}
	}
	for _, page := range sortedPages(existing) {
		queue.add(pageCandidate{ID: page.ID, FetchRef: page.ID, Page: page})
	}
	collections, err := st.Collections(ctx)
	if err != nil {
		return summary, err
	}
	for _, collection := range collections {
		queue.add(pageCandidate{ID: collection.ID, FetchRef: collection.ID})
	}

	now := store.NowMS()
	cursor := "discover:" + startedAt.Format(time.RFC3339)
	for queue.remaining() > 0 {
		if opts.Limit > 0 && summary.Candidates >= opts.Limit {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("Notion MCP discovery stopped at limit %d with %d references not visited", opts.Limit, queue.remaining()))
			break
		}
		candidate := queue.pop()
		if !candidate.Refresh {
			synced, err := st.HasSyncState(ctx, store.SourceNotionMCP, "page_content", candidate.ID)
			if err != nil {
				return summary, err
			}
			if synced {
				stored, ok, err := st.Block(ctx, markdownBlockID(candidate.ID))
				if err != nil {
					return summary, err
				}
				if ok {
					enqueueLinks(queue, fetchResult{Text: stored.Text}, candidate.ID, existing)
				}
				continue
			}
		}
		summary.Candidates++
		result, err := gateway.fetchPage(ctx, tools.Fetch, candidate.FetchRef)
		if err != nil {
			summary.Failed++
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("Notion MCP fetch failed for %s: %v", candidate.ID, err))
			continue
		}
		enqueueLinks(queue, result, candidate.ID, existing)
		candidate.Cursor = cursor
		if err := persistFetch(ctx, st, candidate, result, now, &summary); err != nil {
			return summary, err
		}
	}
	if err := finishSummary(summary); err != nil {
		return summary, err
	}
	if err := st.SetSyncState(ctx, store.SourceNotionMCP, discoveryEntity, discoveryLastRunKey, startedAt.Format(time.RFC3339)); err != nil {
		return summary, err
	}
	return summary, nil
}

func sweepRecentEdits(
	ctx context.Context,
	gateway *gatewayClient,
	tools notionToolset,
	since time.Time,
	existing map[string]store.Page,
	queue *crawlQueue,
	summary *Summary,
) {
	filter := lastEditedFilter(tools.SearchSchema, since)
	if filter == nil {
		summary.Warnings = append(summary.Warnings, "Notion MCP search tool exposes no last-edited date filter; recent edits are discovered only through links and --query")
		return
	}
	results, err := gateway.searchPages(ctx, tools.Search, "", sweepPageSize(tools.SearchSchema), filter)
	if err != nil {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf("Notion MCP recent-edit search failed: %v", err))
		return
	}
	for _, result := range results {
		candidate := searchCandidate(result, existing)
		known, isKnown := existing[candidate.ID]
		edited := parseTimestamp(result.Timestamp)
		candidate.Refresh = !isKnown || edited == 0 || edited > known.LastEditedTime
		if edited > candidate.Page.LastEditedTime {
			candidate.Page.LastEditedTime = edited
		}
		queue.add(candidate)
	}
}

func searchCandidate(result searchResult, existing map[string]store.Page) pageCandidate {
	id := pageIDFromReference(result.ID)
	if id == "" {
		id = result.ID
	}
	page := existing[id]
	if page.ID == "" {
		page = store.Page{
			ID:             id,
			Title:          result.Title,
			URL:            result.URL,
			LastEditedTime: parseTimestamp(result.Timestamp),
		}
	}
	return pageCandidate{ID: id, FetchRef: result.ID, Page: page}
}

func discoverySince(ctx context.Context, st *store.Store, now time.Time) (time.Time, error) {
	cursor, ok, err := st.SyncStateCursor(ctx, store.SourceNotionMCP, discoveryEntity, discoveryLastRunKey)
	if err != nil {
		return time.Time{}, err
	}
	if ok {
		if last, err := time.Parse(time.RFC3339, cursor); err == nil {
			return last.Add(-sweepOverlap), nil
		}
	}
	return now.Add(-firstSweepWindow), nil
}

func sortedPages(existing map[string]store.Page) []store.Page {
	out := make([]store.Page, 0, len(existing))
	for _, page := range existing {
		out = append(out, page)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastEditedTime != out[j].LastEditedTime {
			return out[i].LastEditedTime > out[j].LastEditedTime
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func enqueueLinks(queue *crawlQueue, result fetchResult, selfID string, existing map[string]store.Page) {
	for _, id := range linkedIDs(result, selfID) {
		queue.add(pageCandidate{ID: id, FetchRef: id, Page: existing[id]})
	}
}

func linkedIDs(result fetchResult, selfID string) []string {
	seen := map[string]bool{selfID: true}
	var out []string
	add := func(ref string) {
		id := pageIDFromReference(ref)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, raw := range urlPattern.FindAllString(result.Text, -1) {
		parsed, err := url.Parse(strings.TrimRight(raw, ").,;]}"))
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if !strings.HasSuffix(host, "notion.so") && !strings.HasSuffix(host, "notion.site") && !strings.HasSuffix(host, "notion.com") {
			continue
		}
		add(parsed.Path)
	}
	for _, match := range collectionRefPattern.FindAllStringSubmatch(result.Text, -1) {
		add(match[1])
	}
	return out
}

func lastEditedFilter(schema json.RawMessage, since time.Time) map[string]any {
	filters := schemaProperties(schemaProperties(schema)["filters"])
	for _, name := range sortedKeys(filters) {
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "edit") || !strings.Contains(lower, "date") {
			continue
		}
		for _, key := range sortedKeys(schemaProperties(filters[name])) {
			if strings.Contains(strings.ToLower(key), "start") {
				return map[string]any{name: map[string]any{key: since.Format(time.RFC3339)}}
			}
		}
	}
	return nil
}

func sweepPageSize(schema json.RawMessage) int {
	var node struct {
		Maximum int `json:"maximum"`
	}
	if raw := schemaProperties(schema)["page_size"]; len(raw) > 0 && json.Unmarshal(raw, &node) == nil && node.Maximum > 0 {
		return node.Maximum
	}
	return defaultSweepPageSize
}

func schemaProperties(schema json.RawMessage) map[string]json.RawMessage {
	if len(schema) == 0 {
		return nil
	}
	var node struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &node); err != nil {
		return nil
	}
	return node.Properties
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func metadataLastEdited(raw json.RawMessage) int64 {
	var meta map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &meta) != nil {
		return 0
	}
	for _, key := range sortedKeys(meta) {
		text, ok := meta[key].(string)
		if !ok || !strings.Contains(strings.ToLower(key), "last_edited") {
			continue
		}
		if edited := parseTimestamp(text); edited > 0 {
			return edited
		}
	}
	return 0
}
