package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/store"
)

func runSearch(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	var parsed notcrawlSearchArgs
	if err := parseKongArgs(&parsed, normalizeSingleDashLongFlags(args, "limit"), "notcrawl search", stdout, io.Discard); err != nil {
		return err
	}
	if parsed.Limit <= 0 {
		return fmt.Errorf("search --limit must be positive")
	}
	st, err := store.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open archive for search (run sync first to create or upgrade an archive): %w", err)
	}
	defer st.Close()
	results, err := st.Search(ctx, strings.Join(parsed.Query, " "), parsed.Limit)
	if err != nil {
		return fmt.Errorf("search archive (run sync first if the archive needs an upgrade): %w", err)
	}
	for _, r := range results {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", searchField(r.Kind), searchField(r.ID), searchField(r.Title), searchField(r.Text))
	}
	return nil
}

type notcrawlSearchArgs struct {
	Limit int      `default:"20" help:"Maximum results."`
	Query []string `arg:"" name:"query" help:"Search query."`
}

func searchField(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func printSearchUsage(w io.Writer) {
	fmt.Fprint(w, `Usage of search:
  notcrawl search [--limit N] QUERY

Flags:
  --limit N   maximum results (default 20)
`)
}
