package notionmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL      = "https://chatgpt.com/backend-api/wham/apps"
	maxToolsListPages   = 100
	maxRateLimitRetries = 5
	maxRetryDelay       = 60 * time.Second
)

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
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Meta        *toolMeta       `json:"_meta"`
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
	Fetch        string
	Search       string
	SearchSchema json.RawMessage
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

func (g *gatewayClient) searchPages(ctx context.Context, toolName, query string, pageSize int, extra map[string]any) ([]searchResult, []string, error) {
	arguments := map[string]any{
		"query":                query,
		"query_type":           "internal",
		"content_search_mode":  "workspace_search",
		"page_size":            pageSize,
		"max_highlight_length": 0,
	}
	for key, value := range extra {
		arguments[key] = value
	}
	raw, err := g.callToolText(ctx, toolName, arguments)
	if err != nil {
		return nil, nil, err
	}
	var result struct {
		Results []searchResult    `json:"results"`
		Notices []json.RawMessage `json:"notices"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, nil, fmt.Errorf("failed to decode Notion MCP search response: %w", err)
	}
	notices := make([]string, 0, len(result.Notices))
	for _, notice := range result.Notices {
		notices = append(notices, string(notice))
	}
	return result.Results, notices, nil
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
	status, raw, err := g.post(ctx, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("Codex apps gateway returned HTTP %d", status)
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
	status, _, err := g.post(ctx, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("Codex apps gateway notification returned HTTP %d", status)
	}
	return nil
}

func (g *gatewayClient) post(ctx context.Context, body []byte) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL, strings.NewReader(string(body)))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+g.auth.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		if g.auth.AccountID != "" {
			req.Header.Set("ChatGPT-Account-ID", g.auth.AccountID)
		}
		resp, err := g.http.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to call Codex apps gateway: %w", err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return 0, nil, fmt.Errorf("failed to read Codex apps gateway response: %w", err)
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable
		if !retryable || attempt >= maxRateLimitRetries {
			return resp.StatusCode, raw, nil
		}
		if err := sleepContext(ctx, retryDelay(resp.Header.Get("Retry-After"), attempt)); err != nil {
			return 0, nil, err
		}
	}
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	retryAfter = strings.TrimSpace(retryAfter)
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, maxRetryDelay)
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return min(max(time.Until(at), 0), maxRetryDelay)
	}
	return min(time.Second<<attempt, maxRetryDelay)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resolveNotionTools(tools []toolDefinition, connectorID string) (notionToolset, error) {
	var fetch, search toolDefinition
	fetchExact, searchExact := false, false
	for _, tool := range tools {
		if !matchesNotionConnector(tool, connectorID) {
			continue
		}
		identity := strings.ToLower(strings.Join([]string{tool.Name, tool.Title, toolMetaURI(tool)}, " "))
		if strings.Contains(identity, "legacy") {
			continue
		}
		name := strings.ToLower(tool.Name)
		switch {
		case strings.HasSuffix(name, "fetch"):
			if !fetchExact || len(tool.Name) < len(fetch.Name) {
				fetch, fetchExact = tool, true
			}
		case strings.HasSuffix(name, "search"):
			if !searchExact || len(tool.Name) < len(search.Name) {
				search, searchExact = tool, true
			}
		case !fetchExact && fetch.Name == "" && strings.Contains(identity, "fetch"):
			fetch = tool
		case !searchExact && search.Name == "" && strings.Contains(identity, "search"):
			search = tool
		}
	}
	if fetch.Name == "" {
		return notionToolset{}, fmt.Errorf("could not resolve fetch tool for the configured Notion connector")
	}
	if search.Name == "" {
		return notionToolset{}, fmt.Errorf("could not resolve search tool for the configured Notion connector")
	}
	return notionToolset{Fetch: fetch.Name, Search: search.Name, SearchSchema: search.InputSchema}, nil
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
