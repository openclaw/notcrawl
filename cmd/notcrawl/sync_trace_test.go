package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSyncVerboseAPITraceAndRedaction(t *testing.T) {
	const private = "private-workspace-marker"
	usersRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/"+private) {
		case "/users":
			usersRequests++
			if usersRequests == 1 {
				w.Header().Set("Retry-After", "0.001")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"message":%q}`, private)
			} else if r.URL.Query().Get("start_cursor") == "" {
				fmt.Fprintf(w, `{"results":[{"id":%q,"name":%q}],"has_more":true,"next_cursor":%q}`, private, private, private)
			} else {
				fmt.Fprint(w, `{"results":[],"has_more":false}`)
			}
		case "/search":
			var body bytes.Buffer
			_, _ = body.ReadFrom(r.Body)
			if strings.Contains(body.String(), `"value":"page"`) {
				fmt.Fprintf(w, `{"results":[{"id":%q,"properties":{"title":{"title":[{"plain_text":%q}]}},"parent":{"type":"workspace","workspace":true}}]}`, private, private)
			} else {
				fmt.Fprint(w, `{"results":[]}`)
			}
		case "/blocks/" + private + "/children":
			fmt.Fprintf(w, `{"results":[{"id":"block-private","type":"paragraph","has_children":true,"paragraph":{"rich_text":[{"plain_text":%q}]}}]}`, private)
		case "/blocks/block-private/children":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"code":"validation_error","message":%q}`, private+" not supported via the API")
		case "/comments":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"code":"restricted_resource","message":%q}`, private)
		default:
			http.Error(w, private, http.StatusNotFound)
		}
	}))
	defer server.Close()
	configPath := syncTraceAPIConfig(t, server.URL+"/"+private)
	var normalOut, normalErr bytes.Buffer
	args := []string{"--config", configPath, "sync", "--source", "api"}
	if err := run(context.Background(), args, &normalOut, &normalErr); err != nil {
		t.Fatal(err)
	}
	usersRequests = 0
	var verboseOut, verboseErr bytes.Buffer
	if err := run(context.Background(), append(args, "--verbose"), &verboseOut, &verboseErr); err != nil {
		t.Fatal(err)
	}
	if verboseOut.String() != normalOut.String() {
		t.Fatalf("verbose changed stdout: %q != %q", verboseOut.String(), normalOut.String())
	}
	logs := verboseErr.String()
	for _, want := range []string{`msg="sync trace"`, `phase=api`, `phase=users`, `phase=pages`, `phase=collections`, `state=started`, `state=finished`, `elapsed=`, `attempt=1`, `attempt=2`, `state=retry`, `status=429`, `status=200`, `status=403`, `retry_after=1ms`, `users=1`, `pages=1`, `blocks=1`, `warnings=1`} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing %q in trace:\n%s", want, logs)
		}
	}
	for _, secret := range []string{private, "block-private", "test-token", server.URL, "Authorization", "Bearer", "start_cursor", "restricted_resource"} {
		if strings.Contains(logs, secret) {
			t.Errorf("private data %q in trace:\n%s", secret, logs)
		}
	}
	if usersRequests != 3 {
		t.Fatalf("users requests = %d, want 3 (retry and pagination)", usersRequests)
	}
	// The flag must not leak into later invocations in the same process.
	usersRequests = 0
	var offOut, offErr bytes.Buffer
	if err := run(context.Background(), append(args, "--verbose=false"), &offOut, &offErr); err != nil {
		t.Fatal(err)
	}
	// Wall-clock progress duration is the only non-deterministic output field.
	elapsed := regexp.MustCompile(`elapsed=[^ ]+`)
	if offOut.String() != normalOut.String() || elapsed.ReplaceAllString(offErr.String(), "elapsed=<time>") != elapsed.ReplaceAllString(normalErr.String(), "elapsed=<time>") {
		t.Fatalf("disabled verbose changed normal output:\nstdout %q / %q\nstderr %q / %q", offOut.String(), normalOut.String(), offErr.String(), normalErr.String())
	}
}

func TestSyncVerboseSanitizesFailure(t *testing.T) {
	const private = "private-error-marker"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"code":%q,"message":%q}`, private, private)
	}))
	defer server.Close()
	configPath := syncTraceAPIConfig(t, server.URL+"/"+private)
	for _, verbose := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		args := []string{"--config", configPath, "sync", "--source", "api", fmt.Sprintf("--verbose=%t", verbose)}
		err := run(context.Background(), args, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected API failure")
		}
		logs := stderr.String() + err.Error()
		if verbose {
			for _, secret := range []string{private, server.URL, "test-token"} {
				if strings.Contains(logs, secret) {
					t.Fatalf("private data %q in verbose failure: %s", secret, logs)
				}
			}
			for _, want := range []string{"status=401", "state=failed", "phase=users", "sync failed"} {
				if !strings.Contains(logs, want) {
					t.Fatalf("missing %q in failure trace: %s", want, logs)
				}
			}
		} else if !strings.Contains(logs, private) || strings.Contains(logs, `msg="sync trace"`) {
			t.Fatalf("normal error output changed: %s", logs)
		}
	}
}

func TestSyncVerboseAllSourcesAndLocalFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`db_path = %q
[notion.desktop]
enabled = true
path = %q
[notion.api]
enabled = false
[notion.mcp]
enabled = false
`, filepath.Join(dir, "archive.db"), filepath.Join(dir, "private-desktop-path"))), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--config", configPath, "sync", "--verbose"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"phase=archive", "phase=desktop", "pages=0", "blocks=0", "phase=sync state=finished"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("missing %q in source trace: %s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), dir) {
		t.Fatalf("local path leaked: %s", stderr.String())
	}
	// Opening a directory as SQLite fails before source ingestion begins.
	stderr.Reset()
	err := run(context.Background(), []string{"--config", configPath, "--db", dir, "sync", "--verbose"}, &stdout, &stderr)
	if err == nil || strings.Contains(stderr.String()+err.Error(), dir) || !strings.Contains(stderr.String(), "phase=archive state=failed") {
		t.Fatalf("unsafe or missing archive failure: %v / %s", err, stderr.String())
	}
}

func syncTraceAPIConfig(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NOTCRAWL_TRACE_TEST_TOKEN", "test-token")
	path := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf("db_path = %q\n[notion.api]\nbase_url = %q\ntoken_env = %q\n", filepath.Join(dir, "archive.db"), baseURL, "NOTCRAWL_TRACE_TEST_TOKEN")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
