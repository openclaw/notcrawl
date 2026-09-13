package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/markdown"
	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
	"github.com/openclaw/notcrawl/internal/tableexport"
)

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

var createExportOutput = func(name string) (io.WriteCloser, error) {
	return os.Create(name)
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
	formatValue := tableexport.Format(*format)
	if err := tableexport.ValidateFormat(formatValue); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	var out io.Writer = stdout
	var file io.WriteCloser
	outputName := ""
	if *output != "" {
		outputPath, err := config.ExpandPath(*output)
		if err != nil {
			return err
		}
		file, err = createExportOutput(outputPath)
		if err != nil {
			return err
		}
		out = file
		outputName = outputPath
	}
	s, exportErr := tableexport.Exporter{Store: st}.Export(ctx, *databaseID, formatValue, out)
	if file != nil {
		closeErr := file.Close()
		if exportErr != nil {
			return exportErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(stdout, "exported %d rows and %d columns from %s to %s\n", s.Rows, s.Columns, s.Database, outputName)
		return nil
	}
	return exportErr
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
	indexWriter := csv.NewWriter(index)
	indexWriter.Comma = '\t'
	if err := indexWriter.Write([]string{"id", "name", "source", "rows", "columns", "file"}); err != nil {
		_ = index.Close()
		return err
	}
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
		if err := indexWriter.Write([]string{collection.ID, collection.Name, collection.Source, fmt.Sprint(s.Rows), fmt.Sprint(s.Columns), name}); err != nil {
			_ = index.Close()
			return err
		}
	}
	indexWriter.Flush()
	if err := indexWriter.Error(); err != nil {
		_ = index.Close()
		return err
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
