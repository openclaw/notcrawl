package notionapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoRetriesClientTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) == 1 {
					if phase == "body" {
						_, _ = io.WriteString(w, `{"results":`)
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, `{"results":[]}`)
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 100 * time.Millisecond
			var out map[string]any
			err := (Client{BaseURL: server.URL, HTTP: client}).do(context.Background(), http.MethodGet, "/comments", nil, &out)
			if err != nil || attempts.Load() != 2 || out["results"] == nil {
				t.Fatalf("timeout recovery: attempts=%d output=%v error=%v", attempts.Load(), out, err)
			}
		})
	}
}

func TestDoTimeoutRetryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		parentError        error
		want               int
	}{
		{"exhausted", http.MethodGet, "/users", nil, maxAPIAttempts},
		{"search", http.MethodPost, "/search", nil, maxAPIAttempts},
		{"database query", http.MethodPost, "/databases/db/query", nil, maxAPIAttempts},
		{"data source query", http.MethodPost, "/data_sources/ds/query", nil, maxAPIAttempts},
		{"write", http.MethodPost, "/pages", nil, 1},
		{"caller canceled", http.MethodGet, "/users", context.Canceled, 1},
		{"caller deadline", http.MethodGet, "/users", context.DeadlineExceeded, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			if tc.parentError == context.Canceled {
				ctx, cancel = context.WithCancel(ctx)
			} else if tc.parentError == context.DeadlineExceeded {
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
			}
			defer cancel()
			attempts := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if tc.method == http.MethodPost {
					body, err := io.ReadAll(req.Body)
					if err != nil || string(body) != `{"page_size":100}` {
						t.Errorf("replayed body=%s error=%v", body, err)
					}
				}
				if tc.parentError == context.Canceled {
					cancel()
				}
				if tc.parentError != nil {
					<-ctx.Done()
				}
				return nil, context.DeadlineExceeded
			})}
			var body any
			if tc.method == http.MethodPost {
				body = map[string]any{"page_size": 100}
			}
			err := (Client{BaseURL: "https://example.test", HTTP: client}).do(ctx, tc.method, tc.path, body, &map[string]any{})
			if !errors.Is(err, context.DeadlineExceeded) || attempts != tc.want {
				t.Fatalf("attempts=%d want=%d error=%v", attempts, tc.want, err)
			}
		})
	}
	if shouldRetryTransportError(context.Background(), http.MethodGet, "/users", context.Canceled) {
		t.Fatal("cancellation must not be retried")
	}
}
