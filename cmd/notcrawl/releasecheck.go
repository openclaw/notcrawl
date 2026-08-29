package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/openclaw/crawlkit/releasecheck"
	"github.com/openclaw/notcrawl/internal/config"
)

const notcrawlUpgradeHint = "brew upgrade openclaw/tap/notcrawl"

func notcrawlReleaseCheckOptions(force bool) releasecheck.Options {
	cfg := config.Default()
	return releasecheck.Options{
		AppName:        "notcrawl",
		Owner:          "openclaw",
		Repo:           "notcrawl",
		CurrentVersion: version,
		CacheDir:       cfg.CacheDir,
		Force:          force,
		Client:         &http.Client{Timeout: 5 * time.Second},
	}
}

func maybeNotifyRelease(ctx context.Context, stderr io.Writer, args []string) {
	_, _ = releasecheck.Notify(ctx, releasecheck.NotifyOptions{
		Options:     notcrawlReleaseCheckOptions(false),
		Stderr:      stderr,
		InstallHint: notcrawlUpgradeHint,
		Args:        args,
		IsTerminal:  releasecheck.StderrIsTerminal(),
	})
}

func runCheckUpdate(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("check-update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "write JSON output")
	force := fs.Bool("force", false, "force a fresh release check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("check-update takes flags only")
	}
	result, err := releasecheck.Check(ctx, notcrawlReleaseCheckOptions(*force))
	if err != nil && !errors.Is(err, releasecheck.ErrSkipped) {
		return err
	}
	if *jsonOut {
		return writeJSON(stdout, result)
	}
	_, err = fmt.Fprint(stdout, releasecheck.StatusText("notcrawl", notcrawlUpgradeHint, result))
	return err
}
