package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/openclaw/crawlkit/progress"
	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/notionapi"
	"github.com/openclaw/notcrawl/internal/notiondesktop"
	"github.com/openclaw/notcrawl/internal/notionmcp"
	"github.com/openclaw/notcrawl/internal/store"
)

func runSync(ctx context.Context, stdout, stderr io.Writer, cfg config.Config, args []string) (err error) {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "all", "source: desktop, api, notion-mcp, all")
	verbose := fs.Bool("verbose", false, "write redacted sync diagnostics to stderr")
	limit := fs.Int("limit", cfg.Notion.MCP.MaxPages, "maximum Notion MCP pages to fetch; 0 means unlimited")
	var pageIDs, queries stringListFlag
	fs.Var(&pageIDs, "page", "Notion page ID or URL to fetch; repeatable")
	fs.Var(&queries, "query", "targeted Notion workspace query; repeatable, maximum 25 results each")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("sync takes flags only")
	}
	if *source != "notion-mcp" && (len(pageIDs) > 0 || len(queries) > 0 || *limit != cfg.Notion.MCP.MaxPages) {
		return fmt.Errorf("--page, --query, and --limit apply only to --source notion-mcp")
	}
	trace := newSyncTrace(stderr, *verbose)
	defer func() { err = trace.finish(err) }()
	trace.start("archive")
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	trace.done()
	progressOutput := stderr
	if *verbose {
		progressOutput = io.Discard
	}
	tracker := progress.New(progressLogger(progressOutput), progress.Options{
		Name:  "sync",
		Unit:  "stages",
		Total: int64(syncStageTotal(*source, cfg)),
		Attrs: []any{"source", *source},
	})
	completed := int64(0)
	defer func() {
		if tracker != nil {
			tracker.Finish(err)
		}
	}()
	sources := []string{*source}
	if *source == "all" {
		sources = nil
		if cfg.Notion.Desktop.Enabled {
			sources = append(sources, "desktop")
		}
		if cfg.Notion.API.Enabled && cfg.APIToken() != "" {
			sources = append(sources, "api")
		}
		if cfg.Notion.MCP.Enabled {
			sources = append(sources, "notion-mcp")
		}
	}
	for _, selectedSource := range sources {
		switch selectedSource {
		case "desktop":
			trace.start("desktop")
			s, err := notiondesktop.Ingest(ctx, st, cfg.Notion.Desktop.Path, cfg.CacheDir, cfg.Notion.Desktop.SpaceIDs)
			if err != nil {
				return err
			}
			completed++
			tracker.Set(completed, "phase", "desktop", "pages", s.Pages, "blocks", s.Blocks, "collections", s.Collections)
			trace.done("pages", s.Pages, "blocks", s.Blocks, "collections", s.Collections, "comments", s.Comments)
			fmt.Fprintf(stdout, "desktop: pages=%d blocks=%d teams=%d collections=%d comments=%d snapshot=%s\n", s.Pages, s.Blocks, s.Teams, s.Collections, s.Comments, s.Source.Snapshot)
		case "api":
			trace.start("api")
			s, err := notionapi.Client{
				BaseURL: cfg.Notion.API.BaseURL,
				Version: cfg.Notion.API.Version,
				Token:   cfg.APIToken(),
				Trace:   trace.apiLogger(),
			}.Sync(ctx, st)
			if err != nil {
				return err
			}
			completed++
			tracker.Set(completed, "phase", "api", "pages", s.Pages, "databases", s.Databases, "blocks", s.Blocks)
			trace.done("users", s.Users, "pages", s.Pages, "databases", s.Databases, "database_rows", s.DatabaseRows, "blocks", s.Blocks, "comments", s.Comments, "warnings", len(s.Warnings))
			if !*verbose {
				writeSyncWarnings(stderr, s.Warnings)
			}
			fmt.Fprintf(stdout, "api: users=%d pages=%d databases=%d database_rows=%d blocks=%d comments=%d\n", s.Users, s.Pages, s.Databases, s.DatabaseRows, s.Blocks, s.Comments)
		case "notion-mcp":
			trace.start("notion-mcp")
			s, err := syncNotionMCP(ctx, st, cfg, notionmcp.SyncOptions{PageIDs: pageIDs, Queries: queries, Limit: *limit})
			if err != nil {
				return err
			}
			completed++
			tracker.Set(completed, "phase", "notion-mcp", "candidates", s.Candidates, "pages", s.Pages, "failed", s.Failed)
			trace.done("candidates", s.Candidates, "pages", s.Pages, "empty", s.EmptyPages, "failed", s.Failed, "warnings", len(s.Warnings))
			if !*verbose {
				writeSyncWarnings(stderr, s.Warnings)
			}
			fmt.Fprintf(stdout, "notion-mcp: candidates=%d pages=%d empty=%d failed=%d\n", s.Candidates, s.Pages, s.EmptyPages, s.Failed)
		default:
			return fmt.Errorf("unknown source %q", *source)
		}
	}

	return nil
}

var newNotionMCPClient = func(cfg config.Config) notionmcp.Client {
	return notionmcp.Client{
		BaseURL:     cfg.Notion.MCP.BaseURL,
		AuthPath:    cfg.Notion.MCP.AuthPath,
		ConnectorID: cfg.Notion.MCP.ConnectorID,
	}
}

func syncNotionMCP(ctx context.Context, st *store.Store, cfg config.Config, opts notionmcp.SyncOptions) (notionmcp.Summary, error) {
	return newNotionMCPClient(cfg).Sync(ctx, st, opts)
}

func writeSyncWarnings(w io.Writer, warnings []string) {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) != "" {
			fmt.Fprintf(w, "warning: %s\n", warning)
		}
	}
}

func progressLogger(w io.Writer) *slog.Logger {
	if w == nil {
		w = io.Discard
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
}

func syncStageTotal(source string, cfg config.Config) int {
	switch source {
	case "desktop", "api", "notion-mcp":
		return 1
	case "all":
		total := 0
		if cfg.Notion.Desktop.Enabled {
			total++
		}
		if cfg.Notion.API.Enabled && cfg.APIToken() != "" {
			total++
		}
		if cfg.Notion.MCP.Enabled {
			total++
		}
		return total
	default:
		return 0
	}
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}
