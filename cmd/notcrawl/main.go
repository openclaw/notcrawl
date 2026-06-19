package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/openclaw/crawlkit/control"
	"github.com/openclaw/crawlkit/progress"
	"github.com/openclaw/crawlkit/tui"
	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/notionapi"
	"github.com/openclaw/notcrawl/internal/notiondesktop"
	"github.com/openclaw/notcrawl/internal/notionmcp"
	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/report"
	"github.com/openclaw/notcrawl/internal/share"
	"github.com/openclaw/notcrawl/internal/store"
	"github.com/openclaw/notcrawl/internal/tableexport"
)

var version = "dev"

const tuiPagePreviewMax = 40

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "notcrawl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || rootHelpRequested(args, "config", "db") {
		printHelp(stdout)
		return nil
	}
	var global notcrawlRootArgs
	if err := parseKongArgs(&global, args, "notcrawl", stdout, stderr); err != nil {
		return err
	}
	if global.Version {
		fmt.Fprintln(stdout, version)
		return nil
	}
	rest := global.Args
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	cmd := rest[0]
	cmdArgs := rest[1:]
	if cmd == "version" {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if cmd == "check-update" {
		return runCheckUpdate(ctx, stdout, stderr, cmdArgs)
	}
	if cmd == "metadata" {
		return runMetadata(stdout)
	}
	if cmd == "init" {
		if hasHelpArg(cmdArgs) {
			printInitUsage(stdout)
			return nil
		}
		path, err := config.WriteStarter(global.Config)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
		return nil
	}
	if cmd == "tui" && hasHelpArg(cmdArgs) {
		return printTUIUsage(stdout)
	}
	if cmd == "search" && hasHelpArg(cmdArgs) {
		printSearchUsage(stdout)
		return nil
	}
	if cmd == "sql" && hasHelpArg(cmdArgs) {
		printSQLUsage(stdout)
		return nil
	}
	maybeNotifyRelease(ctx, stderr, rest)
	cfg, err := config.Load(global.Config)
	if err != nil {
		return err
	}
	if global.DB != "" {
		cfg.DBPath, err = config.ExpandPath(global.DB)
		if err != nil {
			return err
		}
	}
	switch cmd {
	case "doctor":
		return runDoctor(ctx, stdout, cfg, cmdArgs)
	case "status":
		return runStatus(ctx, stdout, cfg, cmdArgs)
	case "report":
		return runReport(ctx, stdout, cfg)
	case "maintain":
		return runMaintain(ctx, stdout, cfg, cmdArgs)
	case "sync":
		return runSync(ctx, stdout, stderr, cfg, cmdArgs)
	case "tap":
		return runSync(ctx, stdout, stderr, cfg, []string{"--source", "desktop"})
	case "export-md":
		return runExportMarkdown(ctx, stdout, cfg)
	case "databases":
		return runDatabases(ctx, stdout, cfg)
	case "export-db":
		return runExportDatabase(ctx, stdout, cfg, cmdArgs)
	case "search":
		return runSearch(ctx, stdout, cfg, cmdArgs)
	case "tui":
		return runTUI(ctx, stdout, cfg, cmdArgs)
	case "sql":
		return runSQL(ctx, stdout, cfg, cmdArgs)
	case "publish":
		return runPublish(ctx, stdout, cfg, cmdArgs)
	case "subscribe":
		return runSubscribe(ctx, stdout, cfg, cmdArgs)
	case "update":
		return runUpdate(ctx, stdout, cfg, cmdArgs)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

type notcrawlRootArgs struct {
	Config  string   `help:"Config file path."`
	DB      string   `name:"db" help:"Database path override."`
	Version bool     `help:"Print version and exit."`
	Args    []string `arg:"" optional:"" passthrough:"partial" name:"command" help:"Command and arguments."`
}

func parseKongArgs(target any, args []string, name string, stdout, stderr io.Writer, options ...kong.Option) error {
	opts := []kong.Option{
		kong.Name(name),
		kong.NoDefaultHelp(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) {}),
	}
	opts = append(opts, options...)
	parser, err := kong.New(target, opts...)
	if err != nil {
		return err
	}
	_, err = parser.Parse(args)
	return err
}

func rootHelpRequested(args []string, valueFlags ...string) bool {
	valueFlagSet := make(map[string]struct{}, len(valueFlags))
	for _, flag := range valueFlags {
		valueFlagSet[flag] = struct{}{}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" || (arg == "help" && i == len(args)-1) {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		if name, ok := strings.CutPrefix(arg, "--"); ok {
			if strings.Contains(name, "=") {
				continue
			}
			if _, ok := valueFlagSet[name]; ok {
				i++
			}
		}
	}
	return false
}

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

func runSync(ctx context.Context, stdout, stderr io.Writer, cfg config.Config, args []string) (err error) {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	source := fs.String("source", "all", "source: desktop, api, notion-mcp, all")
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
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	tracker := progress.New(progressLogger(stderr), progress.Options{
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
	switch *source {
	case "desktop":
		s, err := notiondesktop.Ingest(ctx, st, cfg.Notion.Desktop.Path, cfg.CacheDir)
		if err != nil {
			return err
		}
		completed++
		tracker.Set(completed, "phase", "desktop", "pages", s.Pages, "blocks", s.Blocks, "collections", s.Collections)
		fmt.Fprintf(stdout, "desktop: pages=%d blocks=%d teams=%d collections=%d comments=%d snapshot=%s\n", s.Pages, s.Blocks, s.Teams, s.Collections, s.Comments, s.Source.Snapshot)
	case "api":
		s, err := notionapi.Client{
			BaseURL: cfg.Notion.API.BaseURL,
			Version: cfg.Notion.API.Version,
			Token:   cfg.APIToken(),
		}.Sync(ctx, st)
		if err != nil {
			return err
		}
		completed++
		tracker.Set(completed, "phase", "api", "pages", s.Pages, "databases", s.Databases, "blocks", s.Blocks)
		writeAPIWarnings(stderr, s)
		fmt.Fprintf(stdout, "api: users=%d pages=%d databases=%d database_rows=%d blocks=%d comments=%d\n", s.Users, s.Pages, s.Databases, s.DatabaseRows, s.Blocks, s.Comments)
	case "notion-mcp":
		s, err := syncNotionMCP(ctx, st, cfg, notionmcp.SyncOptions{PageIDs: pageIDs, Queries: queries, Limit: *limit})
		if err != nil {
			return err
		}
		completed++
		tracker.Set(completed, "phase", "notion-mcp", "candidates", s.Candidates, "pages", s.Pages, "failed", s.Failed)
		writeNotionMCPWarnings(stderr, s)
		fmt.Fprintf(stdout, "notion-mcp: candidates=%d pages=%d empty=%d failed=%d\n", s.Candidates, s.Pages, s.EmptyPages, s.Failed)
	case "all":
		if cfg.Notion.Desktop.Enabled {
			s, err := notiondesktop.Ingest(ctx, st, cfg.Notion.Desktop.Path, cfg.CacheDir)
			if err != nil {
				return err
			}
			completed++
			tracker.Set(completed, "phase", "desktop", "pages", s.Pages, "blocks", s.Blocks, "collections", s.Collections)
			fmt.Fprintf(stdout, "desktop: pages=%d blocks=%d teams=%d collections=%d comments=%d snapshot=%s\n", s.Pages, s.Blocks, s.Teams, s.Collections, s.Comments, s.Source.Snapshot)
		}
		if cfg.Notion.API.Enabled && cfg.APIToken() != "" {
			s, err := notionapi.Client{
				BaseURL: cfg.Notion.API.BaseURL,
				Version: cfg.Notion.API.Version,
				Token:   cfg.APIToken(),
			}.Sync(ctx, st)
			if err != nil {
				return err
			}
			completed++
			tracker.Set(completed, "phase", "api", "pages", s.Pages, "databases", s.Databases, "blocks", s.Blocks)
			writeAPIWarnings(stderr, s)
			fmt.Fprintf(stdout, "api: users=%d pages=%d databases=%d database_rows=%d blocks=%d comments=%d\n", s.Users, s.Pages, s.Databases, s.DatabaseRows, s.Blocks, s.Comments)
		}
		if cfg.Notion.MCP.Enabled {
			s, err := syncNotionMCP(ctx, st, cfg, notionmcp.SyncOptions{Limit: cfg.Notion.MCP.MaxPages})
			if err != nil {
				return err
			}
			completed++
			tracker.Set(completed, "phase", "notion-mcp", "candidates", s.Candidates, "pages", s.Pages, "failed", s.Failed)
			writeNotionMCPWarnings(stderr, s)
			fmt.Fprintf(stdout, "notion-mcp: candidates=%d pages=%d empty=%d failed=%d\n", s.Candidates, s.Pages, s.EmptyPages, s.Failed)
		}
	default:
		return fmt.Errorf("unknown source %q", *source)
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

func writeNotionMCPWarnings(w io.Writer, s notionmcp.Summary) {
	for _, warning := range s.Warnings {
		if strings.TrimSpace(warning) != "" {
			fmt.Fprintf(w, "warning: %s\n", warning)
		}
	}
}

func writeAPIWarnings(w io.Writer, s notionapi.Summary) {
	for _, warning := range s.Warnings {
		if strings.TrimSpace(warning) == "" {
			continue
		}
		fmt.Fprintf(w, "warning: %s\n", warning)
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

func runExportMarkdown(ctx context.Context, stdout io.Writer, cfg config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	s, err := markdown.Exporter{Store: st, Dir: cfg.MarkdownDir}.Export(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "exported %d pages to %s", s.Pages, cfg.MarkdownDir)
	if s.IncompletePages > 0 {
		fmt.Fprintf(stdout, " (%d incomplete; %d missing block references)", s.IncompletePages, s.MissingBlockReferences)
	}
	fmt.Fprintln(stdout)
	return nil
}

func runDatabases(ctx context.Context, stdout io.Writer, cfg config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	collections, err := st.Collections(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "id\tname\tsource")
	for _, collection := range collections {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", collection.ID, collection.Name, collection.Source)
	}
	return nil
}

func runExportDatabase(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("export-db", flag.ContinueOnError)
	databaseID := fs.String("database", "", "database id to export")
	all := fs.Bool("all", false, "export every crawled database")
	dir := fs.String("dir", "", "directory for --all exports")
	format := fs.String("format", "csv", "output format: csv or tsv")
	output := fs.String("output", "", "output file path, defaults to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *all {
		if *databaseID != "" {
			return fmt.Errorf("export-db cannot combine --all and --database")
		}
		if *output != "" {
			return fmt.Errorf("export-db cannot combine --all and --output")
		}
		if *dir == "" {
			return fmt.Errorf("export-db --all requires --dir")
		}
		return runExportAllDatabases(ctx, stdout, cfg, tableexport.Format(*format), *dir)
	}
	if *databaseID == "" {
		return fmt.Errorf("export-db requires --database")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	var out io.Writer = stdout
	var file *os.File
	if *output != "" {
		outputPath, err := config.ExpandPath(*output)
		if err != nil {
			return err
		}
		file, err = os.Create(outputPath)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}
	s, err := tableexport.Exporter{Store: st}.Export(ctx, *databaseID, tableexport.Format(*format), out)
	if err != nil {
		return err
	}
	if *output != "" {
		fmt.Fprintf(stdout, "exported %d rows and %d columns from %s to %s\n", s.Rows, s.Columns, s.Database, file.Name())
	}
	return nil
}

func runExportAllDatabases(ctx context.Context, stdout io.Writer, cfg config.Config, format tableexport.Format, dir string) error {
	ext, err := exportExtension(format)
	if err != nil {
		return err
	}
	dir, err = config.ExpandPath(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	collections, err := st.Collections(ctx)
	if err != nil {
		return err
	}
	index, err := os.Create(filepath.Join(dir, "index.tsv"))
	if err != nil {
		return err
	}
	fmt.Fprintln(index, "id\tname\tsource\trows\tcolumns\tfile")
	exporter := tableexport.Exporter{Store: st}
	used := map[string]bool{}
	var databases, rows int
	for _, collection := range collections {
		name := exportDatabaseFilename(collection, ext, used)
		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		if err != nil {
			_ = index.Close()
			return err
		}
		s, exportErr := exporter.Export(ctx, collection.ID, format, file)
		closeErr := file.Close()
		if exportErr != nil {
			_ = index.Close()
			return exportErr
		}
		if closeErr != nil {
			_ = index.Close()
			return closeErr
		}
		databases++
		rows += s.Rows
		fmt.Fprintf(index, "%s\t%s\t%s\t%d\t%d\t%s\n", collection.ID, collection.Name, collection.Source, s.Rows, s.Columns, name)
	}
	if err := index.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "exported %d databases and %d rows to %s\n", databases, rows, dir)
	return nil
}

func exportExtension(format tableexport.Format) (string, error) {
	switch format {
	case "", tableexport.FormatCSV:
		return "csv", nil
	case tableexport.FormatTSV:
		return "tsv", nil
	default:
		return "", fmt.Errorf("unsupported format %q", format)
	}
}

func exportDatabaseFilename(collection store.Collection, ext string, used map[string]bool) string {
	baseName := collection.Name
	if strings.TrimSpace(baseName) == "" {
		baseName = collection.ID
	}
	base := notiontext.Slug(baseName) + "-" + notiontext.ShortID(collection.ID)
	name := base + "." + ext
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d.%s", base, i, ext)
	}
	used[name] = true
	return name
}

func runSearch(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	var parsed notcrawlSearchArgs
	if err := parseKongArgs(&parsed, normalizeSingleDashLongFlags(args, "limit"), "notcrawl search", stdout, io.Discard); err != nil {
		return err
	}
	if parsed.Limit <= 0 {
		return fmt.Errorf("search --limit must be positive")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	results, err := st.Search(ctx, strings.Join(parsed.Query, " "), parsed.Limit)
	if err != nil {
		return err
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

func runTUI(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage of tui:")
		fs.PrintDefaults()
		_, _ = fmt.Fprintln(fs.Output())
		_, _ = fmt.Fprintln(fs.Output(), tui.ControlsHelp())
	}
	if hasHelpArg(args) {
		fs.SetOutput(stdout)
	}
	limit := fs.Int("limit", 200, "maximum rows to load")
	kind := fs.String("kind", "all", "rows to browse: all, pages, databases")
	jsonOut := fs.Bool("json", false, "print browser rows as JSON instead of opening the terminal UI")
	if len(args) == 1 && args[0] == "help" {
		fs.Usage()
		return nil
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("tui takes flags only")
	}
	if *limit <= 0 {
		return fmt.Errorf("tui --limit must be positive")
	}
	rows, err := tuiRows(ctx, cfg, *kind, *limit)
	if err != nil {
		return err
	}
	refreshRows := func(ctx context.Context) ([]tui.Row, error) {
		return tuiRows(ctx, cfg, *kind, *limit)
	}
	return tui.Browse(ctx, tui.BrowseOptions{
		AppName:        "notcrawl",
		Title:          "notcrawl archive",
		EmptyMessage:   "notcrawl has no local pages or databases yet",
		Rows:           rows,
		Refresh:        refreshRows,
		JSON:           *jsonOut,
		Layout:         tui.LayoutDocument,
		SourceKind:     archiveSourceKind(cfg),
		SourceLocation: archiveSourceLocation(cfg),
		Stdout:         stdout,
	})
}

func archiveSourceKind(cfg config.Config) string {
	if strings.TrimSpace(cfg.Share.Remote) != "" {
		return tui.SourceRemote
	}
	return tui.SourceLocal
}

func archiveSourceLocation(cfg config.Config) string {
	if strings.TrimSpace(cfg.Share.Remote) != "" {
		return cfg.Share.Remote
	}
	return cfg.DBPath
}

func tuiRows(ctx context.Context, cfg config.Config, kind string, limit int) ([]tui.Row, error) {
	st, err := store.OpenReadOnly(cfg.DBPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []tui.Row{}, nil
		}
		return nil, err
	}
	defer st.Close()
	pageTitles, _ := st.PageTitles(ctx)
	spaceNames, _ := st.SpaceNames(ctx)
	blockParents, _ := st.BlockParents(ctx)
	collections, err := st.Collections(ctx)
	if err != nil {
		return nil, err
	}
	collectionNames := collectionNameMap(collections)
	var rows []tui.Row
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "all":
		pages, err := st.Pages(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, pageTUIRows(pages, limit, pageTitles, collectionNames, spaceNames, blockParents, pagePreviews(ctx, st, pages, limit))...)
		rows = append(rows, collectionTUIRows(collections, collectionBrowserLimit(limit), pageTitles, collectionNames, spaceNames)...)
	case "pages", "page":
		pages, err := st.Pages(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, pageTUIRows(pages, limit, pageTitles, collectionNames, spaceNames, blockParents, pagePreviews(ctx, st, pages, limit))...)
	case "databases", "database", "collections", "collection":
		rows = append(rows, collectionTUIRows(collections, limit, pageTitles, collectionNames, spaceNames)...)
	default:
		return nil, fmt.Errorf("unknown tui kind %q", kind)
	}
	return rows, nil
}

func collectionBrowserLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit < 50 {
		return limit
	}
	return 50
}

func pageTUIRows(pages []store.Page, limit int, pageTitles map[string]string, collectionNames map[string]string, spaceNames map[string]string, blockParents map[string]store.ParentRef, previews map[string]string) []tui.Row {
	if limit > len(pages) {
		limit = len(pages)
	}
	items := make([]tui.Row, 0, limit)
	for _, page := range pages[:limit] {
		title := strings.TrimSpace(page.Title)
		if title == "" {
			title = page.ID
		}
		space := firstNonEmpty(spaceNames[page.SpaceID], page.SpaceID)
		parent := cleanNotionParentLabel(firstNonEmpty(notionParentLabel(page.ParentTable, page.ParentID, pageTitles, collectionNames, spaceNames, blockParents), notionWorkspaceParent(space)), space)
		preview := previews[page.ID]
		items = append(items, tui.Row{
			Source:    "notion",
			Kind:      "page",
			ID:        page.ID,
			ParentID:  parent,
			Scope:     space,
			Container: firstNonEmpty(collectionNames[page.CollectionID], page.CollectionID),
			Title:     title,
			Text:      preview,
			Detail:    preview,
			URL:       page.URL,
			CreatedAt: formatMillis(page.CreatedTime),
			UpdatedAt: formatMillis(page.LastEditedTime),
			Tags:      []string{page.Source},
			Fields: map[string]string{
				"collection_id": page.CollectionID,
				"parent_id":     page.ParentID,
				"parent_table":  page.ParentTable,
				"source":        page.Source,
				"space_id":      page.SpaceID,
			},
		})
	}
	return items
}

func collectionTUIRows(collections []store.Collection, limit int, pageTitles map[string]string, collectionNames map[string]string, spaceNames map[string]string) []tui.Row {
	if limit > len(collections) {
		limit = len(collections)
	}
	items := make([]tui.Row, 0, limit)
	for _, collection := range collections[:limit] {
		title := strings.TrimSpace(collection.Name)
		if title == "" {
			title = collection.ID
		}
		space := firstNonEmpty(spaceNames[collection.SpaceID], collection.SpaceID)
		parent := cleanNotionParentLabel(firstNonEmpty(notionParentLabel(collection.ParentTable, collection.ParentID, pageTitles, collectionNames, spaceNames, nil), notionWorkspaceParent(space)), space)
		preview := collectionPreview(collection, space, parent)
		items = append(items, tui.Row{
			Source:    "notion",
			Kind:      "database",
			ID:        collection.ID,
			ParentID:  parent,
			Scope:     space,
			Title:     title,
			Text:      preview,
			Detail:    preview,
			UpdatedAt: formatMillis(collection.SyncedAt),
			Tags:      []string{collection.Source},
			Fields: map[string]string{
				"parent_id":    collection.ParentID,
				"parent_table": collection.ParentTable,
				"source":       collection.Source,
				"space_id":     collection.SpaceID,
			},
		})
	}
	return items
}

func pagePreviews(ctx context.Context, st *store.Store, pages []store.Page, limit int) map[string]string {
	if limit > len(pages) {
		limit = len(pages)
	}
	out := make(map[string]string, limit)
	for _, page := range pages[:limit] {
		blocks, err := st.PageBlocks(ctx, page.ID)
		if err != nil {
			continue
		}
		comments, err := st.PageComments(ctx, page.ID)
		if err != nil {
			comments = nil
		}
		out[page.ID] = pagePreview(blocks, comments, tuiPagePreviewMax)
	}
	return out
}

func pagePreview(blocks []store.Block, comments []store.Comment, maxLines int) string {
	lines := blockPreviewLines(blocks, maxLines)
	remaining := maxLines - len(lines)
	if remaining > 1 && len(comments) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
			remaining--
		}
		lines = append(lines, "## Comments")
		remaining--
		for _, comment := range comments {
			text := notiontext.CleanLegacyArtifacts(comment.Text)
			if text == "" {
				continue
			}
			lines = append(lines, "- "+text)
			remaining--
			if remaining <= 0 {
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func blockPreview(blocks []store.Block, maxLines int) string {
	return strings.Join(blockPreviewLines(blocks, maxLines), "\n")
}

func blockPreviewLines(blocks []store.Block, maxLines int) []string {
	if maxLines <= 0 {
		maxLines = 10
	}
	lines := make([]string, 0, maxLines)
	for _, block := range blocks {
		if block.Type == store.BlockTypeNotionMCPMarkdown {
			for _, line := range strings.Split(strings.Trim(block.Text, "\r\n"), "\n") {
				line = strings.TrimRight(line, " ")
				if line == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
					continue
				}
				lines = append(lines, line)
				if len(lines) >= maxLines {
					return lines
				}
			}
			continue
		}
		text := compactPreviewNoise(notiontext.CleanLegacyArtifacts(block.Text))
		if text == "" {
			continue
		}
		prefix := ""
		switch block.Type {
		case "header", "heading_1":
			prefix = "# "
		case "sub_header", "heading_2":
			prefix = "## "
		case "sub_sub_header", "heading_3":
			prefix = "### "
		case "bulleted_list":
			prefix = "- "
		case "to_do":
			prefix = "- [ ] "
		case "numbered_list":
			prefix = "1. "
		case "quote":
			prefix = "> "
		case "code":
			prefix = "    "
		}
		lines = append(lines, prefix+text)
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func compactPreviewNoise(s string) string {
	s = strings.ReplaceAll(s, "linked pagess", "linked pages")
	for strings.Contains(s, "linked page, linked page") ||
		strings.Contains(s, "linked pages, linked page") ||
		strings.Contains(s, "linked page, linked pages") ||
		strings.Contains(s, "linked pages, linked pages") {
		s = strings.ReplaceAll(s, "linked pages, linked page", "linked pages")
		s = strings.ReplaceAll(s, "linked page, linked pages", "linked pages")
		s = strings.ReplaceAll(s, "linked pages, linked pages", "linked pages")
		s = strings.ReplaceAll(s, "linked page, linked page", "linked pages")
		s = strings.ReplaceAll(s, "linked pagess", "linked pages")
	}
	return s
}

func collectionPreview(collection store.Collection, space, parent string) string {
	var lines []string
	if space != "" {
		lines = append(lines, "Workspace: "+space)
	}
	if parent != "" {
		lines = append(lines, "Parent: "+parent)
	}
	if strings.TrimSpace(collection.SchemaJSON) != "" {
		lines = append(lines, "Schema captured")
	}
	if len(lines) == 0 {
		lines = append(lines, "Database metadata captured from Notion.")
	}
	return strings.Join(lines, "\n")
}

func notionWorkspaceParent(space string) string {
	space = strings.TrimSpace(space)
	if space == "" {
		return ""
	}
	return "Workspace: " + space
}

func collectionNameMap(collections []store.Collection) map[string]string {
	out := make(map[string]string, len(collections))
	for _, collection := range collections {
		name := strings.TrimSpace(collection.Name)
		if name == "" {
			name = collection.ID
		}
		out[collection.ID] = name
	}
	return out
}

func notionParentLabel(parentTable, parentID string, pageTitles map[string]string, collectionNames map[string]string, spaceNames map[string]string, blockParents map[string]store.ParentRef) string {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return ""
	}
	parentTable = strings.TrimSpace(parentTable)
	if strings.HasPrefix(parentID, "space:") {
		parentID = strings.TrimPrefix(parentID, "space:")
		parentTable = "space"
	}
	seen := map[string]bool{}
	for depth := 0; depth < 16 && parentID != ""; depth++ {
		key := parentTable + ":" + parentID
		if seen[key] {
			return ""
		}
		seen[key] = true
		switch parentTable {
		case "page":
			return firstNonEmpty(pageTitles[parentID], readableNotionParentFallback(parentID))
		case "block":
			if title := firstNonEmpty(pageTitles[parentID], ""); title != "" {
				return title
			}
			if blockParents != nil {
				if parent, ok := blockParents[parentID]; ok {
					parentTable = strings.TrimSpace(parent.Table)
					parentID = strings.TrimSpace(parent.ID)
					continue
				}
			}
			return readableNotionParentFallback(parentID)
		case "collection", "database", "data_source":
			if collectionNames != nil {
				return firstNonEmpty(collectionNames[parentID], readableNotionParentFallback(parentID))
			}
		case "space", "team", "workspace":
			if spaceNames != nil {
				if name := firstNonEmpty(spaceNames[parentID], ""); name != "" {
					return notionWorkspaceParent(name)
				}
			}
		}
		return firstNonEmpty(readableNotionParentFallback(parentID), "")
	}
	return ""
}

func readableNotionParentFallback(parentID string) string {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" || looksLikeNotionID(parentID) {
		return ""
	}
	return parentID
}

func cleanNotionParentLabel(label, workspace string) string {
	label = strings.TrimSpace(label)
	if label == "" || noisyNotionLabel(label) {
		return notionWorkspaceParent(workspace)
	}
	return label
}

func noisyNotionLabel(label string) bool {
	label = strings.TrimSpace(label)
	if label == "" || looksLikeNotionID(label) {
		return true
	}
	seenID := false
	for _, field := range strings.Fields(label) {
		token := strings.Trim(field, ".,;:()[]{}<>\"'")
		if looksLikeNotionID(token) {
			if seenID {
				return true
			}
			seenID = true
		}
	}
	return seenID && len([]rune(label)) > 80
}

func looksLikeNotionID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 24 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func searchField(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func runSQL(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	var parsed notcrawlSQLArgs
	if err := parseKongArgs(&parsed, args, "notcrawl sql", stdout, io.Discard); err != nil {
		return err
	}
	if len(parsed.Query) == 0 {
		return fmt.Errorf("sql query required")
	}
	query := strings.TrimSpace(strings.Join(parsed.Query, " "))
	if !isReadOnlyQuery(query) {
		return fmt.Errorf("only read-only select/with/pragma queries are allowed")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.DB().QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printRows(stdout, rows)
}

type notcrawlSQLArgs struct {
	Query []string `arg:"" optional:"" passthrough:"all" name:"query" help:"Read-only SQL query."`
}

func printSearchUsage(w io.Writer) {
	fmt.Fprint(w, `Usage of search:
  notcrawl search [--limit N] QUERY

Flags:
  --limit N   maximum results (default 20)
`)
}

func printSQLUsage(w io.Writer) {
	fmt.Fprint(w, `Usage of sql:
  notcrawl sql QUERY

Runs a read-only SELECT, WITH, or PRAGMA query against the local archive.
`)
}

func runPublish(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	remote := fs.String("remote", cfg.Share.Remote, "git remote")
	repo := fs.String("repo", cfg.Share.RepoPath, "share repo path")
	branch := fs.String("branch", cfg.Share.Branch, "share branch")
	message := fs.String("message", "archive: notcrawl snapshot", "commit message")
	tag := fs.String("tag", "", "immutable snapshot tag")
	push := fs.Bool("push", false, "push after commit")
	noCommit := fs.Bool("no-commit", false, "write snapshot without committing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := (markdown.Exporter{Store: st, Dir: cfg.MarkdownDir}).Export(ctx); err != nil {
		return err
	}
	s, err := share.Publish(ctx, st, share.PublishOptions{
		RepoPath: *repo, Remote: *remote, Branch: *branch, MarkdownDir: cfg.MarkdownDir,
		Message: *message, Push: *push, Commit: !*noCommit, Tag: *tag,
	})
	if err != nil {
		return err
	}
	if s.Tag == "" {
		fmt.Fprintf(stdout, "published %d tables to %s committed=%t pushed=%t\n", len(s.Manifest.Tables), *repo, s.Committed, s.Pushed)
	} else {
		fmt.Fprintf(stdout, "published %d tables to %s committed=%t pushed=%t tag=%s\n", len(s.Manifest.Tables), *repo, s.Committed, s.Pushed, s.Tag)
	}
	return nil
}

func runSubscribe(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("subscribe", flag.ContinueOnError)
	repo := fs.String("repo", cfg.Share.RepoPath, "share repo path")
	branch := fs.String("branch", cfg.Share.Branch, "share branch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remote := cfg.Share.Remote
	if fs.NArg() > 0 {
		remote = fs.Arg(0)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	manifest, err := share.Subscribe(ctx, st, remote, *repo, *branch)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "subscribed %s tables=%d generated_at=%s\n", remote, len(manifest.Tables), manifest.GeneratedAt)
	return nil
}

func runUpdate(ctx context.Context, stdout io.Writer, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	repo := fs.String("repo", cfg.Share.RepoPath, "share repo path")
	branch := fs.String("branch", cfg.Share.Branch, "share branch")
	ref := fs.String("ref", "", "historical tag, commit, or branch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	manifest, resolvedRef, err := share.UpdateAt(ctx, st, cfg.Share.Remote, *repo, *branch, *ref)
	if err != nil {
		return err
	}
	if resolvedRef == "" {
		fmt.Fprintf(stdout, "updated tables=%d generated_at=%s\n", len(manifest.Tables), manifest.GeneratedAt)
	} else {
		fmt.Fprintf(stdout, "updated ref=%s tables=%d generated_at=%s\n", resolvedRef, len(manifest.Tables), manifest.GeneratedAt)
	}
	return nil
}

func printRows(w io.Writer, rows *sql.Rows) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, value := range values {
			if i > 0 {
				fmt.Fprint(w, "\t")
			}
			switch x := value.(type) {
			case nil:
				fmt.Fprint(w, "")
			case []byte:
				fmt.Fprint(w, string(x))
			default:
				fmt.Fprint(w, x)
			}
		}
		fmt.Fprintln(w)
	}
	return rows.Err()
}

func hasHelpArg(args []string) bool {
	for _, arg := range args {
		if arg == "help" || arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func normalizeSingleDashLongFlags(args []string, names ...string) []string {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	out := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "--") || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "-=") {
			out[i] = arg
			continue
		}
		name := strings.TrimPrefix(arg, "-")
		if before, _, ok := strings.Cut(name, "="); ok {
			name = before
		}
		if _, ok := allowed[name]; ok {
			out[i] = "-" + arg
			continue
		}
		out[i] = arg
	}
	return out
}

func printTUIUsage(stdout io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Int("limit", 200, "maximum rows to load")
	fs.String("kind", "all", "rows to browse: all, pages, databases")
	fs.Bool("json", false, "print browser rows as JSON instead of opening the terminal UI")
	_, _ = fmt.Fprintln(fs.Output(), "Usage of tui:")
	fs.PrintDefaults()
	_, _ = fmt.Fprintln(fs.Output())
	_, _ = fmt.Fprintln(fs.Output(), tui.ControlsHelp())
	return nil
}

func printInitUsage(stdout io.Writer) {
	fmt.Fprint(stdout, `Usage of init:
  notcrawl [global flags] init

Writes a starter TOML config to --config or the standard notcrawl config path.
`)
}

func isReadOnlyQuery(query string) bool {
	lower := strings.ToLower(strings.TrimSpace(query))
	return strings.HasPrefix(lower, "select ") || strings.HasPrefix(lower, "with ") || strings.HasPrefix(lower, "pragma ")
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage of notcrawl:
  notcrawl [global flags] <command> [args]

Global flags:
  --config PATH   config file path
  --db PATH       database path override
  --version       print version and exit

Commands:
  metadata                  Print crawlkit control metadata
  check-update              Check for a newer notcrawl release
  version                   Print version
  init                      Write a starter config
  doctor                    Check config, database, desktop cache, and token
  status                    Show archive counts and database size
  report                    Show recent archive activity
  maintain [--vacuum]       Rebuild FTS and optimize SQLite indexes
  sync --source desktop     Ingest Notion Desktop cache
  sync --source api         Ingest through the official Notion API
  sync --source notion-mcp  Repair known pages through the Codex Notion connector
  sync --source all         Run enabled sources
  tap                       Legacy-friendly alias for sync --source desktop
  export-md                 Render normalized Markdown from SQLite
  databases                 List crawled Notion databases
  export-db --database ID   Export a database as CSV or TSV
  export-db --all --dir DIR Export every database as CSV or TSV
  search [--limit N] QUERY  Search page text
  tui                       Browse pages and databases in the terminal UI
  sql QUERY                 Run read-only SQL
  publish [--push] [--tag NAME]
                            Export data and Markdown into a git share repo
  subscribe REMOTE          Clone/import a git share repo
  update [--ref REF]        Pull/import current or historical git share data
`)
}
