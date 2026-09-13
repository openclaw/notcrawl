package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/kong"
)

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

func printInitUsage(stdout io.Writer) {
	fmt.Fprint(stdout, `Usage of init:
  notcrawl [global flags] init

Writes a starter TOML config to --config or the standard notcrawl config path.
`)
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
  subscribe [--restore] [--retain-revisions] REMOTE
                            Clone and merge a git share repo
  update [--ref REF] [--restore] [--retain-revisions]
                            Pull and merge current or historical git share data
`)
}
