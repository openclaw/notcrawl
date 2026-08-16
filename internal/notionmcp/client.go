package notionmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/notcrawl/internal/store"
)

const (
	defaultBaseURL    = "https://chatgpt.com/backend-api/wham/apps"
	maxToolsListPages = 100
)

var (
	pageIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}`)
	urlPattern    = regexp.MustCompile(`https?://[^\s<>"']+`)
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
		page.RawJSON = fetchMetadataJSON(result)
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
			RawJSON:        fetchMetadataJSON(result),
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
			RawJSON:     fetchMetadataJSON(result),
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

func markdownBlockID(pageID string) string {
	return "notion-mcp:" + pageID
}

func pageIDFromReference(ref string) string {
	match := pageIDPattern.FindString(ref)
	if match == "" {
		return ""
	}
	compact := strings.ReplaceAll(strings.ToLower(match), "-", "")
	if len(compact) != 32 {
		return ""
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
}

func parseTimestamp(raw string) int64 {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func extractEnhancedMarkdown(text string) string {
	content, hasContent := taggedContent(text, "content")
	if !hasContent {
		return strings.Trim(text, "\r\n")
	}
	var sections []string
	if properties, ok := taggedContent(text, "properties"); ok && strings.TrimSpace(properties) != "" {
		var formatted strings.Builder
		formatted.WriteString("## Properties\n\n")
		for _, line := range strings.Split(strings.Trim(properties, "\r\n"), "\n") {
			formatted.WriteString("    ")
			formatted.WriteString(line)
			formatted.WriteByte('\n')
		}
		sections = append(sections, strings.TrimRight(formatted.String(), "\n"))
	}
	if strings.TrimSpace(content) != "" {
		sections = append(sections, strings.Trim(content, "\r\n"))
	}
	return strings.Join(sections, "\n\n")
}

func taggedContent(text, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	end := strings.LastIndex(text, close)
	if start < 0 || end < start {
		return "", false
	}
	return text[start+len(open) : end], true
}

func fetchMetadataJSON(result fetchResult) string {
	metadata := sanitizeJSONURLs(result.Metadata)
	payload := struct {
		Metadata json.RawMessage `json:"metadata,omitempty"`
		Title    string          `json:"title,omitempty"`
		URL      string          `json:"url,omitempty"`
	}{
		Metadata: metadata,
		Title:    result.Title,
		URL:      sanitizeSignedURLs(result.URL),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func sanitizeJSONURLs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	value = sanitizeJSONValue(value)
	sanitized, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return sanitized
}

func sanitizeJSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeSignedURLs(typed)
	case []any:
		for i := range typed {
			typed[i] = sanitizeJSONValue(typed[i])
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = sanitizeJSONValue(item)
		}
		return typed
	default:
		return value
	}
}

func sanitizeSignedURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		candidate := raw
		trailing := ""
		for len(candidate) > 0 && strings.ContainsRune(").,;]}", rune(candidate[len(candidate)-1])) {
			trailing = candidate[len(candidate)-1:] + trailing
			candidate = candidate[:len(candidate)-1]
		}
		parsed, err := url.Parse(strings.ReplaceAll(candidate, "&amp;", "&"))
		if err != nil {
			return raw
		}
		sensitive := false
		for key := range parsed.Query() {
			lower := strings.ToLower(key)
			normalized := strings.ReplaceAll(lower, "-", "_")
			if strings.HasPrefix(lower, "x-amz-") ||
				strings.Contains(lower, "signature") ||
				strings.Contains(lower, "credential") ||
				strings.Contains(lower, "security-token") ||
				normalized == "token" || strings.HasSuffix(normalized, "_token") ||
				normalized == "jwt" || normalized == "api_key" ||
				normalized == "apikey" || normalized == "client_secret" ||
				lower == "sig" || lower == "policy" || lower == "key-pair-id" {
				sensitive = true
				break
			}
		}
		if !sensitive {
			return raw
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String() + trailing
	})
}

type authInfo struct {
	AccessToken string
	AccountID   string
}

type rpcEnvelope struct {
	Result *json.RawMessage `json:"result"`
	Error  *rpcError        `json:"error"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type toolMeta struct {
	ConnectorID   string `json:"connector_id"`
	ConnectorName string `json:"connector_name"`
	ResourceURI   string `json:"resource_uri"`
}

type toolDefinition struct {
	Name  string    `json:"name"`
	Title string    `json:"title"`
	Meta  *toolMeta `json:"_meta"`
}

type toolsListResult struct {
	Tools      []toolDefinition `json:"tools"`
	NextCursor string           `json:"nextCursor"`
}

type toolCallResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type notionToolset struct {
	Fetch  string
	Search string
}

type gatewayClient struct {
	http    *http.Client
	baseURL string
	auth    authInfo
}

func (c Client) gateway(ctx context.Context) (*gatewayClient, notionToolset, error) {
	baseURL, err := c.validatedBaseURL()
	if err != nil {
		return nil, notionToolset{}, err
	}
	auth, err := resolveAuth(c.AuthPath)
	if err != nil {
		return nil, notionToolset{}, err
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	gateway := &gatewayClient{http: httpClient, baseURL: baseURL, auth: auth}
	if err := gateway.rpcCall(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "notcrawl",
			"version": "dev",
		},
	}, nil); err != nil {
		return nil, notionToolset{}, err
	}
	if err := gateway.rpcNotify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return nil, notionToolset{}, err
	}
	tools, err := gateway.listAllTools(ctx)
	if err != nil {
		return nil, notionToolset{}, err
	}
	resolved, err := resolveNotionTools(tools, c.ConnectorID)
	if err != nil {
		return nil, notionToolset{}, err
	}
	return gateway, resolved, nil
}

func (c Client) validatedBaseURL() (string, error) {
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if c.AllowUnsafeBaseURL {
		return baseURL, nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid Codex apps gateway URL: %w", err)
	}
	if parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), "chatgpt.com") ||
		parsed.Port() != "" ||
		parsed.EscapedPath() != "/backend-api/wham/apps" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", fmt.Errorf("refusing to send Codex credentials to untrusted apps gateway %q", baseURL)
	}
	return defaultBaseURL, nil
}

func (g *gatewayClient) listAllTools(ctx context.Context) ([]toolDefinition, error) {
	var all []toolDefinition
	cursor := ""
	seen := map[string]bool{}
	for range maxToolsListPages {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page toolsListResult
		if err := g.rpcCall(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if strings.TrimSpace(page.NextCursor) == "" {
			return all, nil
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("MCP tools/list repeated cursor %q", page.NextCursor)
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("MCP tools/list exceeded %d pages", maxToolsListPages)
}

func (g *gatewayClient) fetchPage(ctx context.Context, toolName, ref string) (fetchResult, error) {
	raw, err := g.callToolText(ctx, toolName, map[string]any{"id": ref})
	if err != nil {
		return fetchResult{}, err
	}
	var result fetchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		result.Text = raw
	}
	if strings.TrimSpace(result.Text) == "" {
		return fetchResult{}, fmt.Errorf("fetch returned no page content")
	}
	return result, nil
}

func (g *gatewayClient) searchPages(ctx context.Context, toolName, query string, pageSize int) ([]searchResult, error) {
	raw, err := g.callToolText(ctx, toolName, map[string]any{
		"query":                query,
		"query_type":           "internal",
		"content_search_mode":  "workspace_search",
		"page_size":            pageSize,
		"max_highlight_length": 0,
	})
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to decode Notion MCP search response: %w", err)
	}
	return result.Results, nil
}

func (g *gatewayClient) callToolText(ctx context.Context, name string, arguments map[string]any) (string, error) {
	var result toolCallResult
	if err := g.rpcCall(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	}, &result); err != nil {
		return "", err
	}
	if result.IsError {
		return "", fmt.Errorf("Notion connector tool %s reported an error", name)
	}
	var parts []string
	for _, item := range result.Content {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("Notion connector tool %s returned no text", name)
	}
	return strings.Join(parts, "\n"), nil
}

func (g *gatewayClient) rpcCall(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.auth.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	if g.auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", g.auth.AccountID)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call Codex apps gateway: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read Codex apps gateway response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Codex apps gateway returned HTTP %d", resp.StatusCode)
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("failed to decode Codex apps gateway response: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("Codex apps gateway JSON-RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result == nil {
		return fmt.Errorf("Codex apps gateway response missing result")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(*envelope.Result, out); err != nil {
		return fmt.Errorf("failed to decode %s response: %w", method, err)
	}
	return nil
}

func (g *gatewayClient) rpcNotify(ctx context.Context, method string, params any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.auth.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	if g.auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", g.auth.AccountID)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to notify Codex apps gateway: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Codex apps gateway notification returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func resolveNotionTools(tools []toolDefinition, connectorID string) (notionToolset, error) {
	var fetch, search string
	for _, tool := range tools {
		if !matchesNotionConnector(tool, connectorID) {
			continue
		}
		identity := strings.ToLower(strings.Join([]string{tool.Name, tool.Title, toolMetaURI(tool)}, " "))
		if strings.Contains(identity, "legacy") {
			continue
		}
		switch {
		case strings.Contains(identity, "fetch"):
			fetch = tool.Name
		case strings.Contains(identity, "search"):
			search = tool.Name
		}
	}
	if fetch == "" {
		return notionToolset{}, fmt.Errorf("could not resolve fetch tool for the configured Notion connector")
	}
	if search == "" {
		return notionToolset{}, fmt.Errorf("could not resolve search tool for the configured Notion connector")
	}
	return notionToolset{Fetch: fetch, Search: search}, nil
}

func matchesNotionConnector(tool toolDefinition, connectorID string) bool {
	if tool.Meta == nil {
		return false
	}
	if strings.TrimSpace(connectorID) != "" {
		return tool.Meta.ConnectorID == connectorID
	}
	return strings.EqualFold(strings.TrimSpace(tool.Meta.ConnectorName), "Notion")
}

func toolMetaURI(tool toolDefinition) string {
	if tool.Meta == nil {
		return ""
	}
	return tool.Meta.ResourceURI
}

func resolveAuth(authPath string) (authInfo, error) {
	if token := strings.TrimSpace(firstNonEmpty(os.Getenv("CODEX_APPS_ACCESS_TOKEN"), os.Getenv("CODEX_CONNECTORS_TOKEN"))); token != "" {
		return authInfo{
			AccessToken: token,
			AccountID:   strings.TrimSpace(os.Getenv("CODEX_APPS_ACCOUNT_ID")),
		}, nil
	}
	path := expandPath(authPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return authInfo{}, fmt.Errorf("failed to read Codex auth file %s: %w", path, err)
	}
	var payload struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return authInfo{}, fmt.Errorf("failed to parse Codex auth file %s: %w", path, err)
	}
	if payload.Tokens == nil || strings.TrimSpace(payload.Tokens.AccessToken) == "" {
		return authInfo{}, fmt.Errorf("Codex auth file %s does not contain tokens.access_token", path)
	}
	return authInfo{
		AccessToken: strings.TrimSpace(payload.Tokens.AccessToken),
		AccountID:   strings.TrimSpace(payload.Tokens.AccountID),
	}, nil
}

func expandPath(raw string) string {
	if raw == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(raw, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
