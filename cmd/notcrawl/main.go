package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/openclaw/notcrawl/internal/config"
)

var version = "dev"

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
		return runTUI(ctx, stdout, config.Config{}, []string{"--help"})
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
