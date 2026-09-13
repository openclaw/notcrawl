package markdown

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
	"github.com/openclaw/notcrawl/internal/tableexport"
)

type propertySpec struct {
	Name string
	Type string
}

type propertyRow struct {
	key   string
	name  string
	value string
}

func writeProperties(b *strings.Builder, paths pathResolver, page store.Page) bool {
	var properties map[string]any
	if err := json.Unmarshal([]byte(page.PropertiesJSON), &properties); err != nil {
		return false
	}
	specs := paths.properties[pageCollectionID(page)]
	var rows []propertyRow
	for key, value := range properties {
		spec := specs[key]
		text := tableexport.PropertyText(value, paths.propertyRefs)
		if text == "" || strings.Trim(text, "‣ ") == "" {
			continue
		}
		// Without schema/type metadata, preserve ambiguous properties. A duplicate
		// heading is safer than silently dropping a real field.
		if spec.Type == "title" || embeddedPropertyType(value) == "title" ||
			(spec.Type == "" && strings.EqualFold(strings.TrimSpace(key), "title") &&
				notiontext.Normalize(text) == notiontext.Normalize(page.Title)) {
			continue
		}
		name := spec.Name
		if name == "" {
			name = key
		}
		rows = append(rows, propertyRow{key: key, name: name, value: text})
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := strings.ToLower(rows[i].name), strings.ToLower(rows[j].name)
		if left != right {
			return left < right
		}
		if rows[i].name != rows[j].name {
			return rows[i].name < rows[j].name
		}
		return rows[i].key < rows[j].key
	})
	if len(rows) == 0 {
		return false
	}
	b.WriteString("## Properties\n\n")
	for _, row := range rows {
		fmt.Fprintf(b, "- **%s:** %s\n", markdownPropertyName(row.name), markdownPropertyValue(row.value))
	}
	b.WriteString("\n")
	return true
}

func markdownPropertyName(name string) string {
	name = strings.Join(strings.Fields(notiontext.MarkdownEscape(name)), " ")
	return strings.NewReplacer(
		`&`, `&amp;`,
		`<`, `&lt;`,
		`>`, `&gt;`,
		`\`, `\\`,
		`*`, `\*`,
		`_`, `\_`,
		`[`, `\[`,
		`]`, `\]`,
		"`", "\\`",
	).Replace(name)
}

func markdownPropertyValue(value string) string {
	value = notiontext.MarkdownEscape(value)
	return strings.ReplaceAll(value, "\n", "<br>")
}

func propertySpecs(raw string) map[string]propertySpec {
	var schema map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return nil
	}
	out := make(map[string]propertySpec, len(schema))
	for key, value := range schema {
		name, _ := value["name"].(string)
		typ, _ := value["type"].(string)
		out[key] = propertySpec{Name: name, Type: typ}
	}
	return out
}

func pageCollectionID(page store.Page) string {
	if page.CollectionID != "" {
		return page.CollectionID
	}
	switch page.ParentTable {
	case "collection", "database", "data_source":
		return page.ParentID
	default:
		return ""
	}
}

func embeddedPropertyType(value any) string {
	property, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := property["type"].(string)
	return typ
}
