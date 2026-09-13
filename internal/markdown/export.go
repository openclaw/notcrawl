package markdown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
)

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
	projected, err := notiontext.ExportFields(map[string]string{
		"source": page.Source,
		"url":    page.URL, "title": page.Title, "icon": page.Icon, "cover": page.Cover,
		"properties_json": page.PropertiesJSON, "raw_json": page.RawJSON,
	})
	if err != nil {
		return "", store.BlockCoverage{}, err
	}
	page.URL, page.Title = projected["url"], projected["title"]
	page.Icon, page.Cover = projected["icon"], projected["cover"]
	page.PropertiesJSON, page.RawJSON = projected["properties_json"], projected["raw_json"]
	for i := range blocks {
		projected, err := notiontext.ExportFields(map[string]string{
			"source": blocks[i].Source,
			"text":   blocks[i].Text, "properties_json": blocks[i].PropertiesJSON,
			"raw_json": blocks[i].RawJSON, "content_json": blocks[i].ContentJSON,
			"format_json": blocks[i].FormatJSON,
		})
		if err != nil {
			return "", store.BlockCoverage{}, err
		}
		blocks[i].Text, blocks[i].PropertiesJSON = projected["text"], projected["properties_json"]
	}
	for i := range comments {
		projected, err := notiontext.ExportFields(map[string]string{"source": comments[i].Source, "text": comments[i].Text, "raw_json": comments[i].RawJSON})
		if err != nil {
			return "", store.BlockCoverage{}, err
		}
		comments[i].Text = projected["text"]
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
	renderBlocks(&b, page.ID, store.PreferredPageContentBlocks(blocks, apiBlocksSynced))
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

func writeFrontMatter(b *strings.Builder, page store.Page, spaceName, teamID, teamName string, coverage store.BlockCoverage) {
	b.WriteString("---\n")
	writeKV(b, "generated_by", "notcrawl")
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

func shouldWriteEmptyDesktopNotice(page store.Page, comments []store.Comment, wroteProperties, wroteBlocks bool) bool {
	return strings.Contains(page.Source, "desktop") && !wroteProperties && !wroteBlocks && len(comments) == 0
}

func writeKV(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, `"`, `\"`)
	fmt.Fprintf(b, "%s: \"%s\"\n", key, value)
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
		if filepath.Ext(path) == ".md" && !keep[path] && isNotcrawlGeneratedMarkdown(path) {
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

func isNotcrawlGeneratedMarkdown(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "\ngenerated_by: \"notcrawl\"\n")
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
