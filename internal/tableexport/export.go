package tableexport

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/openclaw/notcrawl/internal/notiontext"
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
	} else if format != "" && format != FormatCSV {
		return Summary{}, fmt.Errorf("unsupported format %q", format)
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
				row = append(row, propertyValueText(props[col.Key], refs))
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

func decodeMap(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func propertyValueText(v any, refs ReferenceLabels) string {
	if text, ok := desktopValueText(v, refs); ok {
		return text
	}
	m, ok := v.(map[string]any)
	if !ok {
		return notiontext.Plain(v)
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		return notiontext.Plain(v)
	}
	switch typ {
	case "title", "rich_text":
		return notiontext.Plain(m[typ])
	case "number":
		return numberText(m["number"])
	case "select", "status":
		return namedObject(m[typ])
	case "multi_select":
		return joinNamed(m[typ])
	case "date":
		return dateText(m["date"])
	case "checkbox":
		if b, ok := m["checkbox"].(bool); ok {
			return strconv.FormatBool(b)
		}
	case "url", "email", "phone_number", "created_time", "last_edited_time":
		if s, ok := m[typ].(string); ok {
			return s
		}
	case "people", "files":
		return joinNamed(m[typ])
	case "relation":
		return joinIDs(m[typ], refs)
	case "formula":
		return formulaText(m["formula"], refs)
	case "rollup":
		return rollupText(m["rollup"], refs)
	case "created_by", "last_edited_by":
		return namedObject(m[typ])
	case "unique_id":
		return uniqueIDText(m["unique_id"])
	}
	return notiontext.Plain(v)
}

func PropertyText(v any, refs ReferenceLabels) string {
	return propertyValueText(v, refs)
}

func desktopValueText(v any, refs ReferenceLabels) (string, bool) {
	text, ok := desktopPlain(v, refs)
	if !ok {
		return "", false
	}
	text = notiontext.Normalize(strings.ReplaceAll(text, " , ", ", "))
	return text, true
}

func desktopPlain(v any, refs ReferenceLabels) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		if x == "‣" {
			return "", true
		}
		return x, true
	case []any:
		if len(x) == 0 {
			return "", true
		}
		if marker, ok := x[0].(string); ok {
			if marker == "‣" && len(x) > 1 {
				return desktopRefListText(x[1], refs), true
			}
			if marker == "," {
				return ",", true
			}
			if marker != "" {
				return marker, true
			}
		}
		parts := make([]string, 0, len(x))
		handled := false
		for _, item := range x {
			text, ok := desktopPlain(item, refs)
			if !ok {
				return "", false
			}
			handled = true
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " "), handled
	default:
		return "", false
	}
}

func desktopRefListText(v any, refs ReferenceLabels) string {
	items, ok := v.([]any)
	if !ok {
		return notiontext.Plain(v)
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := desktopRefText(item, refs); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func desktopRefText(v any, refs ReferenceLabels) string {
	item, ok := v.([]any)
	if !ok || len(item) == 0 {
		return notiontext.Plain(v)
	}
	typ, _ := item[0].(string)
	switch typ {
	case ",":
		return ","
	case "u":
		if id, ok := stringAt(item, 1); ok {
			return labelOrID(refs.Users, id)
		}
	case "p":
		if id, ok := stringAt(item, 1); ok {
			return labelOrID(refs.Pages, id)
		}
	case "d":
		if len(item) > 1 {
			return dateText(item[1])
		}
	}
	return notiontext.Plain(v)
}

func stringAt(items []any, index int) (string, bool) {
	if index >= len(items) {
		return "", false
	}
	s, ok := items[index].(string)
	return s, ok
}

func labelOrID(labels map[string]string, id string) string {
	if label := labels[id]; label != "" {
		return label
	}
	return id
}

func namedObject(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if name, ok := m["name"].(string); ok {
		return name
	}
	if value, ok := m["value"].(string); ok {
		return value
	}
	if id, ok := m["id"].(string); ok {
		return id
	}
	return notiontext.Plain(v)
}

func joinNamed(v any) string {
	items, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := namedObject(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ", ")
}

func joinIDs(v any, refs ReferenceLabels) string {
	items, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := m["id"].(string); ok {
			parts = append(parts, labelOrID(refs.Pages, id))
		}
	}
	return strings.Join(parts, ", ")
}

func dateText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	start, _ := m["start"].(string)
	if start == "" {
		start, _ = m["start_date"].(string)
	}
	end, _ := m["end"].(string)
	if end == "" {
		end, _ = m["end_date"].(string)
	}
	if end != "" {
		return start + "/" + end
	}
	return start
}

func formulaText(v any, refs ReferenceLabels) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "string":
		s, _ := m["string"].(string)
		return s
	case "number":
		return numberText(m["number"])
	case "boolean":
		if b, ok := m["boolean"].(bool); ok {
			return strconv.FormatBool(b)
		}
	case "date":
		return dateText(m["date"])
	}
	if text, ok := desktopValueText(v, refs); ok {
		return text
	}
	return notiontext.Plain(v)
}

func rollupText(v any, refs ReferenceLabels) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "number":
		return numberText(m["number"])
	case "date":
		return dateText(m["date"])
	case "array":
		items, _ := m["array"].([]any)
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text := propertyValueText(item, refs); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	}
	if text, ok := desktopValueText(v, refs); ok {
		return text
	}
	return notiontext.Plain(v)
}

func uniqueIDText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	prefix, _ := m["prefix"].(string)
	number := numberText(m["number"])
	return prefix + number
}

func numberText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}
