package markdown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

type Exporter struct {
	Store *store.Store
	Dir   string
}

type Summary struct {
	Pages                  int
	IncompletePages        int
	MissingBlockReferences int
	Files                  []string
}

func (e Exporter) Export(ctx context.Context) (Summary, error) {
	if e.Store == nil {
		return Summary{}, fmt.Errorf("missing store")
	}
	if e.Dir == "" {
		return Summary{}, fmt.Errorf("missing markdown dir")
	}
	if err := os.MkdirAll(e.Dir, 0o755); err != nil {
		return Summary{}, err
	}
	pages, err := e.Store.Pages(ctx)
	if err != nil {
		return Summary{}, err
	}
	paths, err := newPathResolver(ctx, e.Store)
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	keep := map[string]bool{}
	for _, page := range pages {
		path, coverage, err := e.writePage(ctx, paths, page)
		if err != nil {
			return s, err
		}
		keep[filepath.Clean(path)] = true
		s.Pages++
		if coverage.Missing > 0 {
			s.IncompletePages++
			s.MissingBlockReferences += coverage.Missing
		}
		s.Files = append(s.Files, path)
	}
	if err := pruneStaleMarkdown(e.Dir, keep); err != nil {
		return s, err
	}
	return s, nil
}

func (e Exporter) writePage(ctx context.Context, paths pathResolver, page store.Page) (string, store.BlockCoverage, error) {
	spaceName := paths.spaceName(page.SpaceID)
	teamID := paths.pageTeamID(page)
	teamName := paths.teamName(teamID)
	blocks, err := e.Store.PageBlocks(ctx, page.ID)
	if err != nil {
		return "", store.BlockCoverage{}, err
	}
	var coverage store.BlockCoverage
	apiBlocksSynced, err := e.Store.HasSyncState(ctx, "api", "page_blocks", page.ID)
	if err != nil {
		return "", store.BlockCoverage{}, err
	}
	mcpContentSynced, err := e.Store.HasSyncState(ctx, store.SourceNotionMCP, "page_content", page.ID)
	if err != nil {
		return "", store.BlockCoverage{}, err
	}
	desktopBacked, err := e.Store.RecordHasLiveSource(ctx, "page", page.ID, "desktop")
	if err != nil {
		return "", store.BlockCoverage{}, err
	}
	if desktopBacked && !apiBlocksSynced && !mcpContentSynced {
		coverage, err = e.Store.PageBlockCoverage(ctx, page.ID)
		if err != nil {
			return "", store.BlockCoverage{}, err
		}
	}
	comments, err := e.Store.PageComments(ctx, page.ID)
	if err != nil {
		return "", store.BlockCoverage{}, err
	}
	spaceSlug := notiontext.Slug(spaceName)
	titleSlug := maxSlug(notiontext.Slug(page.Title), 96)
	name := fmt.Sprintf("%s-%s.md", titleSlug, notiontext.ShortID(page.ID))
	parts := []string{e.Dir, spaceSlug}
	if teamName != "" {
		parts = append(parts, notiontext.Slug(teamName))
	}
	parts = append(parts, name)
	path := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", store.BlockCoverage{}, err
	}
	var b strings.Builder
	writeFrontMatter(&b, page, spaceName, teamID, teamName, coverage)
	if page.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", notiontext.MarkdownEscape(page.Title))
	}
	writeCoverageWarning(&b, coverage)
	wroteProperties := writeProperties(&b, paths, page)
	beforeBlocks := b.Len()
	renderBlocks(&b, page.ID, renderableBlocks(blocks, apiBlocksSynced))
	wroteBlocks := b.Len() > beforeBlocks
	if shouldWriteEmptyDesktopNotice(page, comments, wroteProperties, wroteBlocks) {
		b.WriteString("> [!NOTE]\n> No body blocks or non-title properties were present in the Desktop cache. The page may be empty or its body may not have been cached.\n\n")
	}
	if len(comments) > 0 {
		if !strings.HasSuffix(b.String(), "\n\n") {
			b.WriteString("\n")
		}
		b.WriteString("## Comments\n\n")
		for _, c := range comments {
			text := notiontext.MarkdownEscape(c.Text)
			if text == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", text)
		}
	}
	out := strings.TrimRight(b.String(), " \n") + "\n"
	return path, coverage, os.WriteFile(path, []byte(out), 0o644)
}

type pathResolver struct {
	spaces       map[string]string
	teams        map[string]string
	blocks       map[string]store.ParentRef
	collections  map[string]store.ParentRef
	properties   map[string]map[string]propertySpec
	propertyRefs tableexport.ReferenceLabels
}

func newPathResolver(ctx context.Context, st *store.Store) (pathResolver, error) {
	spaces, err := st.SpaceNames(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	teams, err := st.TeamNames(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	blocks, err := st.BlockParents(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	collections, err := st.CollectionParents(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	collectionRows, err := st.Collections(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	properties := make(map[string]map[string]propertySpec, len(collectionRows))
	for _, collection := range collectionRows {
		properties[collection.ID] = propertySpecs(collection.SchemaJSON)
	}
	users, err := st.UserNames(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	pages, err := st.PageTitles(ctx)
	if err != nil {
		return pathResolver{}, err
	}
	return pathResolver{
		spaces:      spaces,
		teams:       teams,
		blocks:      blocks,
		collections: collections,
		properties:  properties,
		propertyRefs: tableexport.ReferenceLabels{
			Users: users,
			Pages: pages,
		},
	}, nil
}

func (r pathResolver) spaceName(id string) string {
	if id == "" {
		return "default"
	}
	if name := r.spaces[id]; name != "" {
		return name
	}
	return "space-" + notiontext.ShortID(id)
}

func (r pathResolver) teamName(id string) string {
	if id == "" {
		return ""
	}
	if name := r.teams[id]; name != "" {
		return name
	}
	return "team-" + notiontext.ShortID(id)
}

func (r pathResolver) pageTeamID(page store.Page) string {
	return r.resolveTeamID(page.ParentTable, page.ParentID, page.CollectionID, map[string]bool{page.ID: true})
}

func (r pathResolver) resolveTeamID(table, id, collectionID string, seen map[string]bool) string {
	if table == "team" {
		return id
	}
	if table == "collection" && id == "" {
		id = collectionID
	}
	if id == "" || seen[table+":"+id] {
		return ""
	}
	seen[table+":"+id] = true
	switch table {
	case "block":
		parent := r.blocks[id]
		return r.resolveTeamID(parent.Table, parent.ID, "", seen)
	case "collection", "database", "data_source":
		parent := r.collections[id]
		return r.resolveTeamID(parent.Table, parent.ID, "", seen)
	default:
		return ""
	}
}

func writeFrontMatter(b *strings.Builder, page store.Page, spaceName, teamID, teamName string, coverage store.BlockCoverage) {
	b.WriteString("---\n")
	writeKV(b, "id", page.ID)
	writeKV(b, "space_id", page.SpaceID)
	writeKV(b, "space", spaceName)
	writeKV(b, "team_id", teamID)
	writeKV(b, "team", teamName)
	writeKV(b, "title", page.Title)
	writeKV(b, "source", page.Source)
	writeKV(b, "notion_url", page.URL)
	writeKV(b, "created_time", formatMS(page.CreatedTime))
	writeKV(b, "last_edited_time", formatMS(page.LastEditedTime))
	if coverage.Missing > 0 {
		fmt.Fprintln(b, "content_complete: false")
		fmt.Fprintf(b, "missing_block_references: %d\n", coverage.Missing)
	}
	b.WriteString("---\n\n")
}

func writeCoverageWarning(b *strings.Builder, coverage store.BlockCoverage) {
	if coverage.Missing == 0 {
		return
	}
	noun := "blocks were"
	if coverage.Missing == 1 {
		noun = "block was"
	}
	fmt.Fprintf(b, "> [!WARNING]\n> Incomplete Desktop cache snapshot: %d referenced %s not available locally. API sync can retrieve content shared with a Notion integration.\n\n", coverage.Missing, noun)
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

func shouldWriteEmptyDesktopNotice(page store.Page, comments []store.Comment, wroteProperties, wroteBlocks bool) bool {
	return strings.Contains(page.Source, "desktop") && !wroteProperties && !wroteBlocks && len(comments) == 0
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

func writeKV(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, `"`, `\"`)
	fmt.Fprintf(b, "%s: \"%s\"\n", key, value)
}

func renderBlocks(b *strings.Builder, pageID string, blocks []store.Block) {
	children := map[string][]store.Block{}
	for _, block := range blocks {
		if block.ID == pageID {
			continue
		}
		parent := block.ParentID
		children[parent] = append(children[parent], block)
	}
	for parent := range children {
		sort.SliceStable(children[parent], func(i, j int) bool {
			a, z := children[parent][i], children[parent][j]
			if a.DisplayOrder != z.DisplayOrder {
				return a.DisplayOrder < z.DisplayOrder
			}
			if a.CreatedTime == z.CreatedTime {
				return a.ID < z.ID
			}
			return a.CreatedTime < z.CreatedTime
		})
	}
	renderChildren(b, pageID, children, 0)
	if len(children[pageID]) == 0 {
		var loose []store.Block
		for _, block := range blocks {
			if block.ID != pageID && block.ParentID != pageID {
				loose = append(loose, block)
			}
		}
		for _, block := range loose {
			renderBlock(b, block, 0)
		}
	}
}

func renderableBlocks(blocks []store.Block, apiBlocksSynced bool) []store.Block {
	hasNotionMCP := false
	for _, block := range blocks {
		if block.Type == store.BlockTypeNotionMCPMarkdown {
			hasNotionMCP = true
			break
		}
	}
	if !hasNotionMCP {
		return blocks
	}
	if apiBlocksSynced {
		out := make([]store.Block, 0, len(blocks))
		for _, block := range blocks {
			if block.Type != store.BlockTypeNotionMCPMarkdown {
				out = append(out, block)
			}
		}
		return out
	}
	var out []store.Block
	for _, block := range blocks {
		if block.Type == store.BlockTypeNotionMCPMarkdown {
			out = append(out, block)
		}
	}
	return out
}

func renderChildren(b *strings.Builder, parentID string, children map[string][]store.Block, depth int) {
	for _, block := range children[parentID] {
		renderBlock(b, block, depth)
		renderChildren(b, block.ID, children, depth+1)
	}
}

func renderBlock(b *strings.Builder, block store.Block, depth int) {
	text := notiontext.MarkdownEscape(block.Text)
	indent := strings.Repeat("  ", depth)
	switch block.Type {
	case store.BlockTypeNotionMCPMarkdown:
		text = strings.Trim(block.Text, "\r\n")
		if text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	case "header", "heading_1":
		writeLine(b, "# "+text)
	case "sub_header", "heading_2":
		writeLine(b, "## "+text)
	case "sub_sub_header", "heading_3":
		writeLine(b, "### "+text)
	case "bulleted_list", "bulleted_list_item":
		writeLine(b, indent+"- "+fallback(text, block.Type))
	case "numbered_list", "numbered_list_item":
		writeLine(b, indent+"1. "+fallback(text, block.Type))
	case "to_do", "to_do_item":
		writeLine(b, indent+"- [ ] "+fallback(text, block.Type))
	case "quote":
		writeLine(b, "> "+fallback(text, block.Type))
	case "code":
		b.WriteString("```text\n")
		b.WriteString(text)
		b.WriteString("\n```\n\n")
	case "divider":
		writeLine(b, "---")
	case "image", "file", "pdf", "video", "figma", "drive":
		writeLine(b, fmt.Sprintf("[%s: %s]", block.Type, fallback(text, block.ID)))
	case "column", "column_list", "table", "table_row", "collection_view":
		if text != "" {
			writeLine(b, text)
		}
	default:
		if text != "" {
			writeLine(b, text)
		} else if block.Type != "" {
			writeLine(b, fmt.Sprintf("[%s]", block.Type))
		}
	}
}

func writeLine(b *strings.Builder, line string) {
	line = strings.TrimRight(line, " ")
	if line == "" {
		return
	}
	b.WriteString(line)
	b.WriteString("\n\n")
}

func fallback(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

func pruneStaleMarkdown(root string, keep map[string]bool) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = filepath.Clean(path)
		if d.IsDir() {
			if path != filepath.Clean(root) {
				dirs = append(dirs, path)
			}
			return nil
		}
		if filepath.Ext(path) == ".md" && !keep[path] {
			return os.Remove(path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !isIgnorableRemoveDirError(err) {
			return err
		}
	}
	return nil
}

func isIgnorableRemoveDirError(err error) bool {
	// os.ErrExist matches EEXIST/ENOTEMPTY on Unix and ERROR_DIR_NOT_EMPTY on
	// Windows; syscall.ENOTEMPTY is an invented constant on Windows and never
	// matches the real error.
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist)
}

func formatMS(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func maxSlug(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if i > max {
			break
		}
		cut = i
	}
	s = strings.TrimRight(s[:cut], "-")
	if s == "" {
		return "untitled"
	}
	return s
}
