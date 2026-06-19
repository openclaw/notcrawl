package notiondesktop

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/cache"
	"github.com/openclaw/notcrawl/internal/notiontext"
	"github.com/openclaw/notcrawl/internal/store"
	_ "modernc.org/sqlite"
)

const (
	SourceName               = "desktop"
	desktopSnapshotRetention = 2
)

type Source struct {
	Path      string
	Snapshot  string
	Available bool
	SizeBytes int64
}

type Summary struct {
	Source      Source
	Spaces      int
	Users       int
	Teams       int
	Pages       int
	Blocks      int
	Collections int
	Comments    int
	RawRecords  int
}

func Inspect(path string) (Source, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Source{Path: path, Available: false}, nil
	}
	if info.IsDir() {
		return Source{Path: path, Available: false}, fmt.Errorf("desktop path is a directory, expected notion.db: %s", path)
	}
	return Source{Path: path, Available: true, SizeBytes: info.Size()}, nil
}

func Ingest(ctx context.Context, st *store.Store, path, cacheDir string) (Summary, error) {
	source, err := Inspect(path)
	if err != nil || !source.Available {
		return Summary{Source: source}, err
	}
	snapshot, err := snapshotDB(path, cacheDir)
	if err != nil {
		return Summary{Source: source}, err
	}
	source.Snapshot = snapshot
	db, err := sql.Open("sqlite", snapshot)
	if err != nil {
		return Summary{Source: source}, err
	}
	defer db.Close()
	s := Summary{Source: source}
	syncedAt := store.NowMS()
	if err := st.WithTransaction(ctx, func() error {
		return st.DeferPageFTS(ctx, func() error {
			if s.Spaces, err = ingestSpaces(ctx, st, db); err != nil {
				return err
			}
			if s.Users, err = ingestUsers(ctx, st, db); err != nil {
				return err
			}
			if s.Teams, err = ingestTeams(ctx, st, db); err != nil {
				return err
			}
			if s.Collections, err = ingestCollections(ctx, st, db); err != nil {
				return err
			}
			if s.Pages, s.Blocks, s.RawRecords, err = ingestBlocks(ctx, st, db, syncedAt); err != nil {
				return err
			}
			if s.Comments, err = ingestComments(ctx, st, db, syncedAt); err != nil {
				return err
			}
			addedSpaces, err := st.EnsureSpaceFallbacks(ctx, SourceName)
			if err != nil {
				return err
			}
			s.Spaces += addedSpaces
			return nil
		})
	}); err != nil {
		return s, err
	}
	if err := st.SetSyncState(ctx, SourceName, "desktop", "notion.db", snapshot); err != nil {
		return s, err
	}
	return s, nil
}

func snapshotDB(path, cacheDir string) (string, error) {
	name := fmt.Sprintf("notion-desktop-%d.db", time.Now().UnixMilli())
	snapshot, err := cache.SnapshotSQLite(cache.SQLiteSnapshotOptions{SourcePath: path, DestinationDir: cacheDir, Name: name})
	if err != nil {
		return "", err
	}
	outPath := snapshot.Path
	if err := pruneDesktopSnapshots(cacheDir, desktopSnapshotRetention, outPath); err != nil {
		return "", err
	}
	return outPath, nil
}

type desktopSnapshot struct {
	path    string
	modTime time.Time
}

func pruneDesktopSnapshots(cacheDir string, keep int, current string) error {
	if keep < 1 {
		keep = 1
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return err
	}
	var snapshots []desktopSnapshot
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "notion-desktop-") || !strings.HasSuffix(name, ".db") {
			continue
		}
		path := filepath.Join(cacheDir, name)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshots = append(snapshots, desktopSnapshot{path: path, modTime: info.ModTime()})
	}
	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].modTime.Equal(snapshots[j].modTime) {
			return snapshots[i].path > snapshots[j].path
		}
		return snapshots[i].modTime.After(snapshots[j].modTime)
	})
	keepPaths := map[string]bool{}
	if current != "" {
		keepPaths[filepath.Clean(current)] = true
	}
	for i := 0; i < len(snapshots) && len(keepPaths) < keep; i++ {
		keepPaths[filepath.Clean(snapshots[i].path)] = true
	}
	for _, snapshot := range snapshots {
		path := filepath.Clean(snapshot.path)
		if keepPaths[path] {
			continue
		}
		for _, target := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func ingestSpaces(ctx context.Context, st *store.Store, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `select id, coalesce(name, ''), coalesce(json_object(
		'id', id, 'name', name, 'pages', pages, 'settings', settings, 'created_time', created_time, 'last_edited_time', last_edited_time
	), '{}') from space`)
	if err != nil {
		return 0, ignoreMissingTable(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, name, raw string
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return n, err
		}
		if name == "" {
			name = id
		}
		if err := st.UpsertSpace(ctx, store.Space{ID: id, Name: name, RawJSON: raw, Source: SourceName, SyncedAt: store.NowMS()}); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func ingestUsers(ctx context.Context, st *store.Store, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `select id, coalesce(name, ''), coalesce(email, ''), coalesce(json_object(
		'id', id, 'name', name, 'email', email, 'given_name', given_name, 'family_name', family_name, 'profile_photo', profile_photo
	), '{}') from notion_user`)
	if err != nil {
		return 0, ignoreMissingTable(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, name, email, raw string
		if err := rows.Scan(&id, &name, &email, &raw); err != nil {
			return n, err
		}
		if err := st.UpsertUser(ctx, store.User{ID: id, Name: name, Email: email, RawJSON: raw, Source: SourceName, SyncedAt: store.NowMS()}); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func ingestTeams(ctx context.Context, st *store.Store, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `select id, space_id, parent_id, parent_table, coalesce(name, ''),
		coalesce(json_object('id', id, 'space_id', space_id, 'parent_id', parent_id, 'parent_table', parent_table,
			'name', name, 'description', description, 'team_pages', team_pages, 'settings', settings), '{}')
		from team where coalesce(archived_at, 0) = 0`)
	if err != nil {
		return 0, ignoreMissingTable(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var x store.Team
		if err := rows.Scan(&x.ID, &x.SpaceID, &x.ParentID, &x.ParentTable, &x.Name, &x.RawJSON); err != nil {
			return n, err
		}
		if x.Name == "" {
			x.Name = x.ID
		}
		x.Source = SourceName
		x.SyncedAt = store.NowMS()
		if err := st.UpsertTeam(ctx, x); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func ingestCollections(ctx context.Context, st *store.Store, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `select id, space_id, parent_id, parent_table, coalesce(name, ''), coalesce(schema, ''), coalesce(format, ''),
		coalesce(json_object('id', id, 'space_id', space_id, 'parent_id', parent_id, 'parent_table', parent_table,
			'name', name, 'schema', schema, 'format', format), '{}')
		from collection where alive = 1`)
	if err != nil {
		return 0, ignoreMissingTable(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var x store.Collection
		if err := rows.Scan(&x.ID, &x.SpaceID, &x.ParentID, &x.ParentTable, &x.Name, &x.SchemaJSON, &x.FormatJSON, &x.RawJSON); err != nil {
			return n, err
		}
		x.Name = notiontext.TitleFromProperties(x.Name)
		if x.Name == "" {
			x.Name = x.ID
		}
		x.Source = SourceName
		x.SyncedAt = store.NowMS()
		if err := st.UpsertCollection(ctx, x); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

type localBlock struct {
	ID             string
	SpaceID        string
	Type           string
	PropertiesJSON string
	ContentJSON    string
	CollectionID   string
	CreatedTime    int64
	LastEditedTime int64
	ParentID       string
	ParentTable    string
	Alive          bool
	FormatJSON     string
	RawJSON        string
	Text           string
}

func ingestBlocks(ctx context.Context, st *store.Store, db *sql.DB, syncedAt int64) (pages int, blocks int, rawRecords int, err error) {
	rows, err := db.QueryContext(ctx, `select id, space_id, type, coalesce(properties, ''), coalesce(content, ''),
		coalesce(collection_id, ''), coalesce(cast(created_time as integer), 0), coalesce(cast(last_edited_time as integer), 0),
		coalesce(parent_id, ''), coalesce(parent_table, ''), alive, coalesce(format, ''),
		coalesce(json_object('id', id, 'space_id', space_id, 'type', type, 'properties', properties, 'content', content,
			'collection_id', collection_id, 'created_time', created_time, 'last_edited_time', last_edited_time,
			'parent_id', parent_id, 'parent_table', parent_table, 'alive', alive, 'format', format), '{}')
		from block`)
	if err != nil {
		return 0, 0, 0, ignoreMissingTable(err)
	}
	defer rows.Close()
	byID := map[string]localBlock{}
	var all []localBlock
	for rows.Next() {
		var b localBlock
		var alive int
		if err := rows.Scan(&b.ID, &b.SpaceID, &b.Type, &b.PropertiesJSON, &b.ContentJSON, &b.CollectionID, &b.CreatedTime,
			&b.LastEditedTime, &b.ParentID, &b.ParentTable, &alive, &b.FormatJSON, &b.RawJSON); err != nil {
			return pages, blocks, rawRecords, err
		}
		b.Alive = alive != 0
		b.Text = blockText(b.PropertiesJSON)
		byID[b.ID] = b
		all = append(all, b)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	pageFor := func(id string) string { return "" }
	var resolve func(string, map[string]bool) string
	resolve = func(id string, seen map[string]bool) string {
		if id == "" || seen[id] {
			return ""
		}
		seen[id] = true
		b, ok := byID[id]
		if !ok {
			return ""
		}
		if isPageType(b.Type) {
			return b.ID
		}
		if b.ParentTable == "block" {
			return resolve(b.ParentID, seen)
		}
		return ""
	}
	pageFor = func(id string) string { return resolve(id, map[string]bool{}) }
	children := childBlocksByParent(all)
	for _, b := range all {
		if isPageType(b.Type) {
			if err := st.UpsertPage(ctx, store.Page{
				ID:             b.ID,
				SpaceID:        b.SpaceID,
				ParentID:       b.ParentID,
				ParentTable:    b.ParentTable,
				CollectionID:   b.CollectionID,
				Title:          pageTitle(b, children),
				PropertiesJSON: b.PropertiesJSON,
				CreatedTime:    b.CreatedTime,
				LastEditedTime: b.LastEditedTime,
				Alive:          b.Alive,
				Source:         SourceName,
				RawJSON:        b.RawJSON,
				SyncedAt:       syncedAt,
			}); err != nil {
				return pages, blocks, rawRecords, err
			}
			pages++
		}
		pageID := pageFor(b.ID)
		if err := st.UpsertBlock(ctx, store.Block{
			ID:             b.ID,
			PageID:         pageID,
			SpaceID:        b.SpaceID,
			ParentID:       b.ParentID,
			ParentTable:    b.ParentTable,
			Type:           b.Type,
			Text:           b.Text,
			PropertiesJSON: b.PropertiesJSON,
			ContentJSON:    b.ContentJSON,
			FormatJSON:     b.FormatJSON,
			CreatedTime:    b.CreatedTime,
			LastEditedTime: b.LastEditedTime,
			Alive:          b.Alive,
			Source:         SourceName,
			RawJSON:        b.RawJSON,
			SyncedAt:       syncedAt,
		}); err != nil {
			return pages, blocks, rawRecords, err
		}
		blocks++
		if err := st.UpsertRawRecord(ctx, store.RawRecord{
			Source: SourceName, RecordTable: "block", RecordID: b.ID, ParentID: b.ParentID,
			SpaceID: b.SpaceID, RawJSON: b.RawJSON, SyncedAt: syncedAt,
		}); err != nil {
			return pages, blocks, rawRecords, err
		}
		rawRecords++
	}
	return pages, blocks, rawRecords, nil
}

func childBlocksByParent(blocks []localBlock) map[string][]localBlock {
	children := map[string][]localBlock{}
	for _, block := range blocks {
		if !block.Alive || block.ParentID == "" {
			continue
		}
		children[block.ParentID] = append(children[block.ParentID], block)
	}
	for parent := range children {
		sort.SliceStable(children[parent], func(i, j int) bool {
			a, z := children[parent][i], children[parent][j]
			if a.CreatedTime == z.CreatedTime {
				return a.ID < z.ID
			}
			return a.CreatedTime < z.CreatedTime
		})
	}
	return children
}

func pageTitle(page localBlock, children map[string][]localBlock) string {
	if title := notiontext.TitleFromProperties(page.PropertiesJSON); title != "" {
		return title
	}
	if title := fallbackPageTitle(page.ID, children, map[string]bool{}); title != "" {
		return title
	}
	return "Untitled"
}

func fallbackPageTitle(parentID string, children map[string][]localBlock, seen map[string]bool) string {
	if parentID == "" || seen[parentID] {
		return ""
	}
	seen[parentID] = true
	for _, child := range children[parentID] {
		if !isPageType(child.Type) {
			if title := titleSnippet(child.Text); title != "" {
				return title
			}
		}
		if title := fallbackPageTitle(child.ID, children, seen); title != "" {
			return title
		}
	}
	return ""
}

func titleSnippet(s string) string {
	s = notiontext.Normalize(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) > 96 {
		return string(runes[:96])
	}
	return s
}

func blockText(raw string) string {
	if title := notiontext.TitleFromProperties(raw); title != "" {
		return title
	}
	return notiontext.PlainFromJSON(raw)
}

func ingestComments(ctx context.Context, st *store.Store, db *sql.DB, syncedAt int64) (int, error) {
	rows, err := db.QueryContext(ctx, `select id, parent_id, space_id, coalesce(text, ''), coalesce(created_by_id, ''),
		coalesce(cast(created_time as integer), 0), coalesce(cast(last_edited_time as integer), 0), alive,
		coalesce(json_object('id', id, 'parent_id', parent_id, 'space_id', space_id, 'text', text, 'content', content,
			'created_by_id', created_by_id, 'created_time', created_time, 'last_edited_time', last_edited_time, 'alive', alive), '{}')
		from comment`)
	if err != nil {
		if ignoreMissingTable(err) == nil {
			return 0, nil
		}
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var c store.Comment
		var alive int
		if err := rows.Scan(&c.ID, &c.ParentID, &c.SpaceID, &c.Text, &c.CreatedByID, &c.CreatedTime, &c.LastEditedTime, &alive, &c.RawJSON); err != nil {
			return n, err
		}
		c.PageID = c.ParentID
		c.Text = notiontext.PlainFromJSON(c.Text)
		c.Alive = alive != 0
		c.Source = SourceName
		c.SyncedAt = syncedAt
		if err := st.UpsertComment(ctx, c); err != nil {
			return n, err
		}
		n++
	}
	// Desktop's comment table is opportunistic and has no completeness marker.
	// Only explicit alive=0 rows retire comments; absence is not a tombstone.
	return n, rows.Err()
}

func isPageType(t string) bool {
	return t == "page" || t == "collection_view_page" || t == "external_object_instance_page"
}

func ignoreMissingTable(err error) error {
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return nil
	}
	return err
}
