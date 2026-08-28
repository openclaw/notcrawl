package notionapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestRequestTraceRedactsTransportAndBodyErrors(t *testing.T) {
	for _, failure := range []string{"transport_error", "read_error", "decode_error"} {
		t.Run(failure, func(t *testing.T) {
			var logs bytes.Buffer
			attempts := 0
			client := Client{
				BaseURL: "https://private-host.invalid/private-base",
				Token:   "test-token",
				Trace:   slog.New(slog.NewTextHandler(&logs, nil)),
				HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					attempts++
					if failure == "transport_error" {
						return nil, errors.New("private-transport-error")
					}
					body := io.ReadCloser(io.NopCloser(strings.NewReader("private-invalid-json")))
					if failure == "read_error" {
						body = &errorAfterDataReadCloser{data: []byte("private-body"), err: errors.New("private-read-error")}
					}
					return &http.Response{StatusCode: 200, Status: "200 private-status", Body: body, Header: http.Header{"Private-Header": []string{"private-header"}}}, nil
				})},
			}
			var out obj
			err := client.do(context.Background(), http.MethodGet, "/blocks/private-page/children?start_cursor=private-cursor", nil, &out)
			if err == nil {
				t.Fatal("expected request failure")
			}
			if strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "test-token") {
				t.Fatalf("trace leaked request or error data: %s", logs.String())
			}
			for _, want := range []string{"operation=blocks.children", "state=" + failure, "attempt=1", "elapsed="} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("missing %q in trace: %s", want, logs.String())
				}
			}
			wantAttempts := 4
			if failure == "decode_error" {
				wantAttempts = 1
			} else if strings.Count(logs.String(), "state=retry") != 3 {
				t.Fatalf("expected three retry events: %s", logs.String())
			}
			if attempts != wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, wantAttempts)
			}
		})
	}
}

func TestTraceOperationNeverReturnsIdentifiers(t *testing.T) {
	for path, want := range map[string]string{
		"/users?start_cursor=private":                   "users.list",
		"/search":                                       "search",
		"/comments?block_id=private":                    "comments.list",
		"/blocks/private/children?start_cursor=private": "blocks.children",
		"/databases/private/query":                      "collections.query",
		"/data_sources/private/query":                   "collections.query",
		"/private?token=private":                        "other",
		"https://private.invalid/users":                 "other",
	} {
		if got := traceOperation(path); got != want {
			t.Errorf("traceOperation(%q) = %q, want %q", path, got, want)
		}
	}
}
