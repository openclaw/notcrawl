package main

import (
	"testing"
	"time"
)

func TestNotcrawlReleaseCheckOptionsSetsHTTPClientTimeout(t *testing.T) {
	opts := notcrawlReleaseCheckOptions(false)
	if opts.Client == nil {
		t.Fatal("Options.Client is nil; crawlkit would fall back to http.DefaultClient")
	}
	if opts.Client.Timeout != 5*time.Second {
		t.Fatalf("Options.Client.Timeout = %v, want 5s", opts.Client.Timeout)
	}
}
