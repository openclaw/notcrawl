package notionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxAPIAttempts = 4

// maxSuccessBodyBytes caps official Notion 2xx bodies. Error responses stay at 4 KiB.
const maxSuccessBodyBytes = 8 << 20

var errSuccessBodyTooLarge = errors.New("response body too large")

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
			responseBody, readErr := readCappedSuccessBody(resp.Body)
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

func readCappedSuccessBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, int64(maxSuccessBodyBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSuccessBodyBytes {
		return nil, errSuccessBodyTooLarge
	}
	return body, nil
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
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, errSuccessBodyTooLarge)
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
