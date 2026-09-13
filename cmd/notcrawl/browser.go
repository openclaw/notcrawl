package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/tui"
	"github.com/openclaw/notcrawl/internal/config"
	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

const tuiPagePreviewMax = 40

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
	return min(limit, 50)
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
			if parent, ok := blockParents[parentID]; ok {
				parentTable = strings.TrimSpace(parent.Table)
				parentID = strings.TrimSpace(parent.ID)
				continue
			}
			return readableNotionParentFallback(parentID)
		case "collection", "database", "data_source":
			return firstNonEmpty(collectionNames[parentID], readableNotionParentFallback(parentID))
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
