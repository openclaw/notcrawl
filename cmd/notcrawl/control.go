package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/control"
	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/notiondesktop"
	"github.com/openclaw/notcrawl/internal/report"
	"github.com/openclaw/notcrawl/internal/store"
)

func runDoctor(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print doctor JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("doctor takes flags only")
	}
	_ = jsonOut
	desktop, err := notiondesktop.Inspect(cfg.Notion.Desktop.Path)
	if err != nil {
		return err
	}
	report := map[string]any{
		"db_path":           cfg.DBPath,
		"cache_dir":         cfg.CacheDir,
		"markdown_dir":      cfg.MarkdownDir,
		"desktop_path":      desktop.Path,
		"desktop_available": desktop.Available,
		"desktop_size":      desktop.SizeBytes,
		"api_token_env":     cfg.Notion.API.TokenEnv,
		"api_token_present": cfg.APIToken() != "",
		"mcp_enabled":       cfg.Notion.MCP.Enabled,
		"mcp_auth_path":     cfg.Notion.MCP.AuthPath,
		"mcp_auth_present":  fileExists(cfg.Notion.MCP.AuthPath),
		"mcp_connector_id":  cfg.Notion.MCP.ConnectorID,
	}
	status := store.Status{DBPath: cfg.DBPath}
	st, err := store.OpenReadOnly(cfg.DBPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		defer st.Close()
		status, err = st.Status(ctx)
		if err != nil {
			return err
		}
	}
	report["status"] = status
	report["api_version"] = cfg.Notion.API.Version
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(b))
	return nil
}

func runStatus(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print normalized crawlkit status JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status takes flags only")
	}
	status := store.Status{DBPath: cfg.DBPath}
	st, err := store.OpenReadOnly(cfg.DBPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		defer st.Close()
		status, err = st.Status(ctx)
		if err != nil {
			return err
		}
	}
	if *jsonOut {
		return writeJSON(stdout, controlStatus(cfg, status))
	}
	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(b))
	return nil
}

func runMetadata(stdout io.Writer) error {
	defaults := config.Default()
	configPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	manifest := control.NewManifest("notcrawl", "Notion Crawl", "notcrawl")
	manifest.Description = "Local-first Notion archive crawler."
	manifest.Branding = control.Branding{SymbolName: "doc.text.magnifyingglass", AccentColor: "#111111", BundleIdentifier: "notion.id"}
	manifest.Paths = control.Paths{
		DefaultConfig:   configPath,
		ConfigEnv:       "NOTCRAWL_CONFIG",
		DefaultDatabase: defaults.DBPath,
		DefaultCache:    defaults.CacheDir,
		DefaultLogs:     filepath.Join(filepath.Dir(defaults.DBPath), "logs"),
		DefaultShare:    defaults.Share.RepoPath,
	}
	manifest.Capabilities = []string{"metadata", "status", "doctor", "sync", "tap", "tui", "git-share", "sql", "markdown", "table-export"}
	manifest.Privacy = control.Privacy{ContainsPrivateMessages: false, ExportsSecrets: false, LocalOnlyScopes: []string{"notion", "desktop-cache", "sqlite", "git-share"}}
	manifest.Commands = map[string]control.Command{
		"status":       {Title: "Status", Argv: []string{"notcrawl", "status", "--json"}, JSON: true},
		"check-update": {Title: "Check for updates", Argv: []string{"notcrawl", "check-update", "--json"}, JSON: true},
		"doctor":       {Title: "Doctor", Argv: []string{"notcrawl", "doctor", "--json"}, JSON: true},
		"sync":         {Title: "Sync", Argv: []string{"notcrawl", "sync", "--source", "all"}, Mutates: true},
		"tap":          {Title: "Import desktop cache", Argv: []string{"notcrawl", "sync", "--source", "desktop"}, Mutates: true},
		"tui":          {Title: "Terminal browser", Argv: []string{"notcrawl", "tui"}},
		"tui-json":     {Title: "Terminal browser rows", Argv: []string{"notcrawl", "tui", "--json"}, JSON: true},
		"publish":      {Title: "Publish share", Argv: []string{"notcrawl", "publish"}, Mutates: true},
		"subscribe":    {Title: "Subscribe share", Argv: []string{"notcrawl", "subscribe"}, Mutates: true},
		"update":       {Title: "Update share", Argv: []string{"notcrawl", "update"}, Mutates: true},
		"export-md":    {Title: "Export Markdown", Argv: []string{"notcrawl", "export-md"}, Mutates: true},
		"databases":    {Title: "List databases", Argv: []string{"notcrawl", "databases"}},
		"export-db":    {Title: "Export database", Argv: []string{"notcrawl", "export-db"}, Mutates: true},
		"legacy-db":    {Title: "Legacy database override", Argv: []string{"notcrawl", "--db"}, Legacy: true},
	}
	return writeJSON(stdout, manifest)
}

func writeJSON(stdout io.Writer, value any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func controlStatus(cfg config.Config, status store.Status) control.Status {
	counts := []control.Count{
		control.NewCount("spaces", "Spaces", int64(status.Spaces)),
		control.NewCount("users", "Users", int64(status.Users)),
		control.NewCount("teams", "Teams", int64(status.Teams)),
		control.NewCount("pages", "Pages", int64(status.Pages)),
		control.NewCount("blocks", "Blocks", int64(status.Blocks)),
		control.NewCount("collections", "Databases", int64(status.Collections)),
		control.NewCount("comments", "Comments", int64(status.Comments)),
		control.NewCount("raw_records", "Raw records", int64(status.RawRecords)),
	}
	out := control.NewStatus("notcrawl", fmt.Sprintf("%d pages and %d databases", status.Pages, status.Collections))
	out.State = "current"
	out.DatabasePath = status.DBPath
	out.DatabaseBytes = status.DBBytes
	out.WALBytes = status.WALBytes
	out.Counts = counts
	if status.LastSyncAt > 0 {
		out.LastSyncAt = time.UnixMilli(status.LastSyncAt).UTC().Format(time.RFC3339)
	}
	out.Share = &control.Share{Enabled: cfg.Share.Remote != "" || cfg.Share.RepoPath != "", RepoPath: cfg.Share.RepoPath, Remote: cfg.Share.Remote, Branch: cfg.Share.Branch}
	out.Databases = append(out.Databases, control.SQLiteDatabase("primary", "Notion archive", "archive", status.DBPath, true, counts))
	out.Databases = append(out.Databases, desktopCacheDatabases(cfg.CacheDir)...)
	return out
}

func desktopCacheDatabases(cacheDir string) []control.Database {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil
	}
	var out []control.Database
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		path := filepath.Join(cacheDir, entry.Name())
		out = append(out, control.SQLiteDatabase("cache-"+strings.TrimSuffix(entry.Name(), ".db"), entry.Name(), "desktop-cache", path, false, nil))
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runReport(ctx context.Context, stdout io.Writer, cfg config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	activity, err := report.Build(ctx, st, report.Options{})
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(activity, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(b))
	return nil
}

func runMaintain(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("maintain", flag.ContinueOnError)
	vacuum := fs.Bool("vacuum", false, "run VACUUM after rebuilding and optimizing indexes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	summary, err := st.Optimize(ctx, *vacuum)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(b))
	return nil
}
