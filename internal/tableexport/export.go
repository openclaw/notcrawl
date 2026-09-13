package tableexport

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/openclaw/notcrawl/internal/store"
)

type Format string

const (
	FormatCSV Format = "csv"
	FormatTSV Format = "tsv"
)

type Exporter struct {
	Store *store.Store
}

type Summary struct {
	Database string
	Rows     int
	Columns  int
}

type exportColumn struct {
	Key    string
	Header string
}

type ReferenceLabels struct {
	Users map[string]string
	Pages map[string]string
}

func (e Exporter) Export(ctx context.Context, databaseID string, format Format, w io.Writer) (Summary, error) {
	if e.Store == nil {
		return Summary{}, fmt.Errorf("missing store")
	}
	if databaseID == "" {
		return Summary{}, fmt.Errorf("database id is required")
	}
	if err := ValidateFormat(format); err != nil {
		return Summary{}, err
	}
	collection, err := e.Store.Collection(ctx, databaseID)
	if err != nil {
		return Summary{}, err
	}
	pages, err := e.Store.CollectionPages(ctx, databaseID)
	if err != nil {
		return Summary{}, err
	}
	refs, err := e.referenceLabels(ctx)
	if err != nil {
		return Summary{}, err
	}
	columns := columnsFor(collection, pages)
	headers := make([]string, 0, len(columns))
	for _, col := range columns {
		headers = append(headers, col.Header)
	}
	writer := csv.NewWriter(w)
	if format == FormatTSV {
		writer.Comma = '\t'
	}
	if err := writer.Write(headers); err != nil {
		return Summary{}, err
	}
	for _, page := range pages {
		props := decodeMap(page.PropertiesJSON)
		row := make([]string, 0, len(columns))
		for _, col := range columns {
			switch col.Key {
			case "page_id":
				row = append(row, page.ID)
			case "page_title":
				row = append(row, page.Title)
			case "url":
				row = append(row, page.URL)
			default:
				row = append(row, PropertyText(props[col.Key], refs))
			}
		}
		if err := writer.Write(row); err != nil {
			return Summary{}, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return Summary{}, err
	}
	return Summary{Database: collection.ID, Rows: len(pages), Columns: len(columns)}, nil
}

func ValidateFormat(format Format) error {
	switch format {
	case "", FormatCSV, FormatTSV:
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func (e Exporter) referenceLabels(ctx context.Context) (ReferenceLabels, error) {
	users, err := e.Store.UserNames(ctx)
	if err != nil {
		return ReferenceLabels{}, err
	}
	pages, err := e.Store.PageTitles(ctx)
	if err != nil {
		return ReferenceLabels{}, err
	}
	return ReferenceLabels{Users: users, Pages: pages}, nil
}

func columnsFor(collection store.Collection, pages []store.Page) []exportColumn {
	seenKeys := map[string]bool{"page_id": true, "page_title": true, "url": true}
	seenHeaders := map[string]bool{"page_id": true, "page_title": true, "url": true}
	cols := []exportColumn{
		{Key: "page_id", Header: "page_id"},
		{Key: "page_title", Header: "page_title"},
		{Key: "url", Header: "url"},
	}
	for _, prop := range schemaProperties(collection.SchemaJSON) {
		if !seenKeys[prop.Key] {
			seenKeys[prop.Key] = true
			prop.Header = uniqueHeader(prop.Header, prop.Key, seenHeaders)
			cols = append(cols, prop)
		}
	}
	var extras []exportColumn
	for _, page := range pages {
		for key := range decodeMap(page.PropertiesJSON) {
			if !seenKeys[key] {
				seenKeys[key] = true
				extras = append(extras, exportColumn{Key: key, Header: key})
			}
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].Header < extras[j].Header
	})
	for i := range extras {
		extras[i].Header = uniqueHeader(extras[i].Header, extras[i].Key, seenHeaders)
	}
	return append(cols, extras...)
}

func schemaProperties(raw string) []exportColumn {
	props := decodeMap(raw)
	var title []exportColumn
	var rest []exportColumn
	for key, value := range props {
		m, ok := value.(map[string]any)
		header := key
		if ok {
			if name, ok := m["name"].(string); ok && strings.TrimSpace(name) != "" {
				header = name
			}
		}
		prop := exportColumn{Key: key, Header: header}
		if ok && m["type"] == "title" {
			title = append(title, prop)
			continue
		}
		rest = append(rest, prop)
	}
	sort.Slice(title, func(i, j int) bool {
		return title[i].Header < title[j].Header
	})
	sort.Slice(rest, func(i, j int) bool {
		return rest[i].Header < rest[j].Header
	})
	return append(title, rest...)
}

func uniqueHeader(header, key string, seen map[string]bool) string {
	if strings.TrimSpace(header) == "" {
		header = key
	}
	if !seen[header] {
		seen[header] = true
		return header
	}
	disambiguated := header + " (" + key + ")"
	for i := 2; seen[disambiguated]; i++ {
		disambiguated = fmt.Sprintf("%s (%s %d)", header, key, i)
	}
	seen[disambiguated] = true
	return disambiguated
}
