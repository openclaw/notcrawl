package notionapi

import (
	"strings"
	"time"
)

// Trace fields come only from fixed labels, durations, and counts. Never pass
// request/response data or errors to the logger, even on failure paths.
func (c Client) tracePhase(phase, state string, started time.Time, counts ...any) {
	if c.Trace == nil {
		return
	}
	attrs := []any{"phase", phase, "state", state, "elapsed", time.Since(started).Round(time.Millisecond)}
	c.Trace.Info("sync trace", append(attrs, counts...)...)
}

func (c Client) traceRequest(path, state string, attempt, statusCode int, started time.Time, retryAfter time.Duration) {
	if c.Trace == nil {
		return
	}
	attrs := []any{
		"phase", "request", "operation", traceOperation(path), "state", state,
		"attempt", attempt, "elapsed", time.Since(started).Round(time.Millisecond),
	}
	if statusCode != 0 {
		attrs = append(attrs, "status", statusCode)
	}
	if state == "retry" {
		attrs = append(attrs, "retry_after", retryAfter)
	}
	c.Trace.Info("sync trace", attrs...)
}

func traceOperation(path string) string {
	path, _, _ = strings.Cut(path, "?")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/users":
		return "users.list"
	case path == "/search":
		return "search"
	case path == "/comments":
		return "comments.list"
	case len(parts) == 3 && parts[0] == "blocks" && parts[2] == "children":
		return "blocks.children"
	case len(parts) == 3 && (parts[0] == "databases" || parts[0] == "data_sources") && parts[2] == "query":
		return "collections.query"
	default:
		return "other"
	}
}
