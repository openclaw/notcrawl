package notionmcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestGatewayRetriesTargetedRepair(t *testing.T) {
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(statusCode)
					return
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{}}`))
			}))
			defer server.Close()
			gateway := &gatewayClient{http: server.Client(), baseURL: server.URL}
			if err := gateway.rpcCall(context.Background(), "tools/call", map[string]any{"name": "fetch"}, nil); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
		})
	}
}

func TestGatewayRetryIsBounded(t *testing.T) {
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(statusCode)
				_, _ = w.Write([]byte("synthetic-secret-payload"))
			}))
			defer server.Close()
			gateway := &gatewayClient{http: server.Client(), baseURL: server.URL}
			err := gateway.rpcCall(context.Background(), "tools/call", nil, nil)
			want := 1
			if statusCode == http.StatusTooManyRequests {
				want = 6
			}
			if attempts != want || err == nil {
				t.Fatalf("attempts=%d want=%d err=%v", attempts, want, err)
			}
			if err.Error() != "Codex apps gateway returned HTTP "+strconv.Itoa(statusCode) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGatewayRetryCancellation(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	gateway := &gatewayClient{http: server.Client(), baseURL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := gateway.rpcCall(ctx, "tools/call", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRetryDelayBounds(t *testing.T) {
	for _, tc := range []struct {
		header  string
		attempt int
		want    time.Duration
	}{
		{"0", 0, 0}, {"3", 0, 3 * time.Second}, {"9223372036854775807", 0, time.Minute}, {"invalid", 2, 4 * time.Second}, {"-1", 0, time.Second},
	} {
		if got := retryDelay(tc.header, tc.attempt); got != tc.want {
			t.Errorf("header=%q got=%v want=%v", tc.header, got, tc.want)
		}
	}
}
