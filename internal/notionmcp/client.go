package notionmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/openclaw/notcrawl/internal/store"
)

type Client struct {
	BaseURL            string
	AuthPath           string
	ConnectorID        string
	HTTPClient         *http.Client
	AllowUnsafeBaseURL bool
}

type SyncOptions struct {
	PageIDs []string
	Queries []string
	Limit   int
}

type Summary struct {
	Candidates int
	Pages      int
	EmptyPages int
	Failed     int
	Warnings   []string
}

type pageCandidate struct {
	ID       string
	FetchRef string
	Page     store.Page
	Cursor   string
}

type searchResult struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

type fetchResult struct {
	Metadata json.RawMessage `json:"metadata"`
	Title    string          `json:"title"`
	URL      string          `json:"url"`
	Text     string          `json:"text"`
}

func (c Client) Sync(ctx context.Context, st *store.Store, opts SyncOptions) (Summary, error) {
	if st == nil {
		return Summary{}, fmt.Errorf("missing store")
	}
	gateway, tools, err := c.gateway(ctx)
	if err != nil {
		return Summary{}, err
	}
	existing, err := existingPages(ctx, st)
	if err != nil {
		return Summary{}, err
	}
	candidates, err := c.candidates(ctx, gateway, tools, st, existing, opts)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Candidates: len(candidates)}
	now := store.NowMS()
	for _, candidate := range candidates {
		result, err := gateway.fetchPage(ctx, tools.Fetch, candidate.FetchRef)
		if err != nil {
			summary.Failed++
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("Notion MCP fetch failed for %s: %v", candidate.ID, err))
			continue
		}
		body := sanitizeSignedURLs(extractEnhancedMarkdown(result.Text))
		if strings.TrimSpace(body) == "" {
			summary.EmptyPages++
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("Notion MCP returned no body for %s; existing archive content was left unchanged", candidate.ID))
			continue
		}
		page := candidate.Page
		if page.ID == "" {
			page.ID = candidate.ID
		}
		if strings.TrimSpace(result.Title) != "" {
			page.Title = result.Title
		}
		if strings.TrimSpace(result.URL) != "" {
			page.URL = sanitizeSignedURLs(result.URL)
		}
		page.URL = sanitizeSignedURLs(page.URL)
		page.Alive = true
		page.Source = store.SourceNotionMCP
		page.SyncedAt = now
		metadata := fetchMetadataJSON(result)
		page.RawJSON = metadata
		if err := st.UpsertPage(ctx, page); err != nil {
			return summary, err
		}
		if err := st.UpsertBlock(ctx, store.Block{
			ID:             markdownBlockID(page.ID),
			PageID:         page.ID,
			SpaceID:        page.SpaceID,
			ParentID:       page.ID,
			ParentTable:    "page",
			Type:           store.BlockTypeNotionMCPMarkdown,
			Text:           body,
			DisplayOrder:   0,
			LastEditedTime: page.LastEditedTime,
			Alive:          true,
			Source:         store.SourceNotionMCP,
			RawJSON:        metadata,
			SyncedAt:       now,
		}); err != nil {
			return summary, err
		}
		if err := st.UpsertRawRecord(ctx, store.RawRecord{
			Source:      store.SourceNotionMCP,
			RecordTable: "page_fetch",
			RecordID:    page.ID,
			ParentID:    page.ParentID,
			SpaceID:     page.SpaceID,
			RawJSON:     metadata,
			SyncedAt:    now,
		}); err != nil {
			return summary, err
		}
		if err := st.SetSyncState(ctx, store.SourceNotionMCP, "page_content", page.ID, candidate.Cursor); err != nil {
			return summary, err
		}
		summary.Pages++
	}
	if summary.Pages == 0 && summary.Failed > 0 {
		return summary, fmt.Errorf("all %d Notion MCP fetches failed", summary.Failed)
	}
	return summary, nil
}

func (c Client) candidates(
	ctx context.Context,
	gateway *gatewayClient,
	tools notionToolset,
	st *store.Store,
	existing map[string]store.Page,
	opts SyncOptions,
) ([]pageCandidate, error) {
	selected := map[string]pageCandidate{}
	add := func(candidate pageCandidate) {
		if candidate.ID == "" {
			return
		}
		if _, ok := selected[candidate.ID]; ok {
			return
		}
		selected[candidate.ID] = candidate
	}
	explicit := len(opts.PageIDs) > 0 || len(opts.Queries) > 0
	for _, ref := range opts.PageIDs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		id := pageIDFromReference(ref)
		if id == "" {
			return nil, fmt.Errorf("could not determine Notion page ID from %q", ref)
		}
		add(pageCandidate{ID: id, FetchRef: ref, Page: existing[id]})
	}
	for _, query := range opts.Queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		pageSize := 25
		if opts.Limit > 0 {
			remaining := opts.Limit - len(selected)
			if remaining <= 0 {
				break
			}
			if remaining < pageSize {
				pageSize = remaining
			}
		}
		results, err := gateway.searchPages(ctx, tools.Search, query, pageSize)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			if result.Type != "" && result.Type != "page" {
				continue
			}
			page := existing[result.ID]
			if page.ID == "" {
				page = store.Page{
					ID:             result.ID,
					Title:          result.Title,
					URL:            result.URL,
					LastEditedTime: parseTimestamp(result.Timestamp),
				}
			}
			add(pageCandidate{ID: result.ID, FetchRef: result.ID, Page: page})
		}
	}
	if !explicit {
		candidates, err := automaticCandidates(ctx, st, existing)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			add(candidate)
		}
	}
	out := make([]pageCandidate, 0, len(selected))
	for _, candidate := range selected {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].Page.LastEditedTime, out[j].Page.LastEditedTime
		if left != right {
			return left > right
		}
		return out[i].ID < out[j].ID
	})
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func automaticCandidates(ctx context.Context, st *store.Store, existing map[string]store.Page) ([]pageCandidate, error) {
	var out []pageCandidate
	for _, canonicalPage := range existing {
		previousCursor, synced, err := st.SyncStateCursor(ctx, store.SourceNotionMCP, "page_content", canonicalPage.ID)
		if err != nil {
			return nil, err
		}
		desktopExists, desktopLive, err := st.RecordSourceState(ctx, "page", canonicalPage.ID, store.SourceDesktop)
		if err != nil {
			return nil, err
		}
		apiExists, apiLive, err := st.RecordSourceState(ctx, "page", canonicalPage.ID, store.SourceAPI)
		if err != nil {
			return nil, err
		}
		if apiLive {
			apiComplete, err := st.HasSyncState(ctx, store.SourceAPI, "page_blocks", canonicalPage.ID)
			if err != nil {
				return nil, err
			}
			if apiComplete {
				if synced {
					if err := retireRepair(ctx, st, canonicalPage.ID); err != nil {
						return nil, err
					}
				}
				continue
			}
		}
		if !desktopLive && !apiLive {
			if synced && (desktopExists || apiExists) {
				if err := retireRepair(ctx, st, canonicalPage.ID); err != nil {
					return nil, err
				}
			}
			continue
		}

		source := store.SourceDesktop
		forceRepair := false
		if apiLive {
			source = store.SourceAPI
			forceRepair = true
		}
		page, ok, err := st.PageForSource(ctx, canonicalPage.ID, source)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		blocks, err := st.PageBlocks(ctx, page.ID)
		if err != nil {
			return nil, err
		}
		cursor, err := sourceSnapshotCursor(page, blocks, source)
		if err != nil {
			return nil, err
		}
		if synced && previousCursor == cursor {
			continue
		}
		if forceRepair {
			out = append(out, pageCandidate{ID: page.ID, FetchRef: page.ID, Page: page, Cursor: cursor})
			continue
		}
		hasBody := false
		for _, block := range blocks {
			if block.ID != page.ID && block.Source == source {
				hasBody = true
				break
			}
		}
		if !hasBody {
			out = append(out, pageCandidate{ID: page.ID, FetchRef: page.ID, Page: page, Cursor: cursor})
			continue
		}
		coverage, err := st.PageBlockCoverage(ctx, page.ID)
		if err != nil {
			return nil, err
		}
		if coverage.Missing > 0 {
			out = append(out, pageCandidate{ID: page.ID, FetchRef: page.ID, Page: page, Cursor: cursor})
			continue
		}
		if synced {
			if err := retireRepair(ctx, st, page.ID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func retireRepair(ctx context.Context, st *store.Store, pageID string) error {
	if _, err := st.RetireSourcePageBlocks(ctx, store.SourceNotionMCP, pageID); err != nil {
		return err
	}
	if err := st.UpsertPage(ctx, store.Page{
		ID:       pageID,
		Alive:    false,
		Source:   store.SourceNotionMCP,
		SyncedAt: store.NowMS(),
	}); err != nil {
		return err
	}
	return st.ClearSyncState(ctx, store.SourceNotionMCP, "page_content", pageID)
}

func sourceSnapshotCursor(page store.Page, blocks []store.Block, source string) (string, error) {
	type snapshotBlock struct {
		ID             string
		ParentID       string
		ParentTable    string
		Type           string
		Text           string
		PropertiesJSON string
		ContentJSON    string
		FormatJSON     string
		DisplayOrder   int64
		CreatedTime    int64
		LastEditedTime int64
	}
	snapshot := struct {
		Page   store.Page
		Blocks []snapshotBlock
	}{
		Page: page,
	}
	snapshot.Page.RawJSON = ""
	snapshot.Page.SyncedAt = 0
	for _, block := range blocks {
		if block.ID == page.ID || block.Source != source {
			continue
		}
		snapshot.Blocks = append(snapshot.Blocks, snapshotBlock{
			ID:             block.ID,
			ParentID:       block.ParentID,
			ParentTable:    block.ParentTable,
			Type:           block.Type,
			Text:           block.Text,
			PropertiesJSON: block.PropertiesJSON,
			ContentJSON:    block.ContentJSON,
			FormatJSON:     block.FormatJSON,
			DisplayOrder:   block.DisplayOrder,
			CreatedTime:    block.CreatedTime,
			LastEditedTime: block.LastEditedTime,
		})
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-sha256:%x", source, sha256.Sum256(raw)), nil
}

func existingPages(ctx context.Context, st *store.Store) (map[string]store.Page, error) {
	pages, err := st.Pages(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.Page, len(pages))
	for _, page := range pages {
		out[page.ID] = page
	}
	return out, nil
}
