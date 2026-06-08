package store

import (
	"context"
	"database/sql"
	"strings"
)

func (s *Store) Pages(ctx context.Context) ([]Page, error) {
	rows, err := s.queryContext(ctx, `select id, space_id, parent_id, parent_table, collection_id, title, url, icon, cover,
		properties_json, created_time, last_edited_time, alive, source, raw_json, synced_at
		from pages where alive = 1 order by coalesce(last_edited_time, 0) desc, title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pages []Page
	for rows.Next() {
		var p Page
		var alive int
		if err := rows.Scan(&p.ID, &p.SpaceID, &p.ParentID, &p.ParentTable, &p.CollectionID, &p.Title, &p.URL, &p.Icon, &p.Cover,
			&p.PropertiesJSON, &p.CreatedTime, &p.LastEditedTime, &alive, &p.Source, &p.RawJSON, &p.SyncedAt); err != nil {
			return nil, err
		}
		p.Alive = IntBool(alive)
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

func (s *Store) Collections(ctx context.Context) ([]Collection, error) {
	rows, err := s.queryContext(ctx, `select id, space_id, parent_id, parent_table, name, schema_json, format_json, raw_json, source, synced_at
		from collections order by lower(coalesce(name, id)), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var collections []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.SpaceID, &c.ParentID, &c.ParentTable, &c.Name, &c.SchemaJSON, &c.FormatJSON, &c.RawJSON, &c.Source, &c.SyncedAt); err != nil {
			return nil, err
		}
		collections = append(collections, c)
	}
	return collections, rows.Err()
}

func (s *Store) Collection(ctx context.Context, id string) (Collection, error) {
	var c Collection
	err := s.queryRowContext(ctx, `select id, space_id, parent_id, parent_table, name, schema_json, format_json, raw_json, source, synced_at
		from collections where id = ?`, id).Scan(&c.ID, &c.SpaceID, &c.ParentID, &c.ParentTable, &c.Name, &c.SchemaJSON, &c.FormatJSON, &c.RawJSON, &c.Source, &c.SyncedAt)
	return c, err
}

func (s *Store) CollectionPages(ctx context.Context, collectionID string) ([]Page, error) {
	rows, err := s.queryContext(ctx, `select id, space_id, parent_id, parent_table, collection_id, title, url, icon, cover,
		properties_json, created_time, last_edited_time, alive, source, raw_json, synced_at
		from pages
		where alive = 1
		  and (collection_id = ? or (parent_id = ? and parent_table in ('collection', 'database', 'data_source')))
		order by coalesce(last_edited_time, 0) desc, title`, collectionID, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pages []Page
	for rows.Next() {
		var p Page
		var alive int
		if err := rows.Scan(&p.ID, &p.SpaceID, &p.ParentID, &p.ParentTable, &p.CollectionID, &p.Title, &p.URL, &p.Icon, &p.Cover,
			&p.PropertiesJSON, &p.CreatedTime, &p.LastEditedTime, &alive, &p.Source, &p.RawJSON, &p.SyncedAt); err != nil {
			return nil, err
		}
		p.Alive = IntBool(alive)
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

func (s *Store) PageBlocks(ctx context.Context, pageID string) ([]Block, error) {
	rows, err := s.queryContext(ctx, `select id, page_id, space_id, parent_id, parent_table, type, text, properties_json,
		content_json, format_json, display_order, created_time, last_edited_time, alive, source, raw_json, synced_at
		from blocks where page_id = ? and alive = 1 order by parent_id, display_order, created_time, id`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blocks []Block
	for rows.Next() {
		var b Block
		var alive int
		if err := rows.Scan(&b.ID, &b.PageID, &b.SpaceID, &b.ParentID, &b.ParentTable, &b.Type, &b.Text, &b.PropertiesJSON,
			&b.ContentJSON, &b.FormatJSON, &b.DisplayOrder, &b.CreatedTime, &b.LastEditedTime, &alive, &b.Source, &b.RawJSON, &b.SyncedAt); err != nil {
			return nil, err
		}
		b.Alive = IntBool(alive)
		blocks = append(blocks, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pageBlocksDisplayOrder(pageID, blocks), nil
}

func (s *Store) PageBlockCoverage(ctx context.Context, pageID string) (BlockCoverage, error) {
	var coverage BlockCoverage
	err := s.queryRowContext(ctx, `with recursive reachable(id) as (
			select id from blocks
			where page_id = ? and alive = 1 and (id = ? or parent_id = ?)
			union
			select child.id
			from blocks child
			join reachable parent on child.parent_id = parent.id
			where child.page_id = ? and child.alive = 1
		),
		referenced as (
			select distinct parent.id as parent_id, child.value as id
			from reachable
			join blocks parent on parent.id = reachable.id
			join json_each(case when json_valid(parent.content_json) then parent.content_json else '[]' end) child
			where child.type = 'text' and child.value <> ''
		)
		select count(*), coalesce(sum(case when available.id is null then 1 else 0 end), 0)
		from referenced
		left join blocks available on available.id = referenced.id and available.parent_id = referenced.parent_id and available.alive = 1`,
		pageID, pageID, pageID, pageID).Scan(&coverage.Referenced, &coverage.Missing)
	return coverage, err
}

func pageBlocksDisplayOrder(pageID string, blocks []Block) []Block {
	children := map[string][]Block{}
	for _, block := range blocks {
		if block.ID == pageID {
			continue
		}
		children[block.ParentID] = append(children[block.ParentID], block)
	}
	for parent := range children {
		sortBlockSiblings(children[parent])
	}

	ordered := make([]Block, 0, len(blocks))
	seen := map[string]struct{}{}
	var appendChildren func(string)
	appendChildren = func(parentID string) {
		for _, block := range children[parentID] {
			if _, ok := seen[block.ID]; ok {
				continue
			}
			seen[block.ID] = struct{}{}
			ordered = append(ordered, block)
			appendChildren(block.ID)
		}
	}
	appendChildren(pageID)
	if len(ordered) == 0 {
		return blocks
	}
	for _, block := range blocks {
		if _, ok := seen[block.ID]; ok || block.ID == pageID {
			continue
		}
		seen[block.ID] = struct{}{}
		ordered = append(ordered, block)
	}
	return ordered
}

func (s *Store) PageComments(ctx context.Context, pageID string) ([]Comment, error) {
	rows, err := s.queryContext(ctx, `select id, page_id, space_id, parent_id, text, created_by_id,
		created_time, last_edited_time, alive, raw_json, source, synced_at
		from comments where page_id = ? and alive = 1 order by created_time, id`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var comments []Comment
	for rows.Next() {
		var c Comment
		var alive int
		if err := rows.Scan(&c.ID, &c.PageID, &c.SpaceID, &c.ParentID, &c.Text, &c.CreatedByID,
			&c.CreatedTime, &c.LastEditedTime, &alive, &c.RawJSON, &c.Source, &c.SyncedAt); err != nil {
			return nil, err
		}
		c.Alive = IntBool(alive)
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *Store) UserNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, `select id, coalesce(nullif(name, ''), nullif(email, ''), id) from users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (s *Store) PageTitles(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, `select id, coalesce(nullif(title, ''), id) from pages where alive = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

func (s *Store) SpaceNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, `select id, name from spaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (s *Store) TeamNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, `select id, name from teams`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (s *Store) BlockParents(ctx context.Context) (map[string]ParentRef, error) {
	rows, err := s.queryContext(ctx, `select id, parent_id, parent_table from blocks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ParentRef{}
	for rows.Next() {
		var id string
		var parentID, parentTable sql.NullString
		if err := rows.Scan(&id, &parentID, &parentTable); err != nil {
			return nil, err
		}
		out[id] = ParentRef{ID: parentID.String, Table: parentTable.String}
	}
	return out, rows.Err()
}

func (s *Store) CollectionParents(ctx context.Context) (map[string]ParentRef, error) {
	rows, err := s.queryContext(ctx, `select id, parent_id, parent_table from collections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ParentRef{}
	for rows.Next() {
		var id string
		var parentID, parentTable sql.NullString
		if err := rows.Scan(&id, &parentID, &parentTable); err != nil {
			return nil, err
		}
		out[id] = ParentRef{ID: parentID.String, Table: parentTable.String}
	}
	return out, rows.Err()
}

func (s *Store) SpaceName(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "default", nil
	}
	var name sql.NullString
	err := s.queryRowContext(ctx, `select name from spaces where id = ?`, id).Scan(&name)
	if err != nil {
		if err == sql.ErrNoRows {
			return fallbackSpaceName(id), nil
		}
		return "", err
	}
	if name.Valid && name.String != "" {
		return name.String, nil
	}
	return fallbackSpaceName(id), nil
}

func (s *Store) PageTeamID(ctx context.Context, page Page) (string, error) {
	seen := map[string]bool{page.ID: true}
	return s.resolveTeamID(ctx, page.ParentTable, page.ParentID, page.CollectionID, seen)
}

func (s *Store) resolveTeamID(ctx context.Context, table, id, collectionID string, seen map[string]bool) (string, error) {
	if table == "team" {
		return id, nil
	}
	if table == "collection" && id == "" {
		id = collectionID
	}
	if id == "" || seen[table+":"+id] {
		return "", nil
	}
	seen[table+":"+id] = true
	switch table {
	case "block":
		var parentID, parentTable sql.NullString
		err := s.queryRowContext(ctx, `select parent_id, parent_table from blocks where id = ?`, id).Scan(&parentID, &parentTable)
		if err != nil {
			if err == sql.ErrNoRows {
				return "", nil
			}
			return "", err
		}
		return s.resolveTeamID(ctx, parentTable.String, parentID.String, "", seen)
	case "collection", "database", "data_source":
		var parentID, parentTable sql.NullString
		err := s.queryRowContext(ctx, `select parent_id, parent_table from collections where id = ?`, id).Scan(&parentID, &parentTable)
		if err != nil {
			if err == sql.ErrNoRows {
				return "", nil
			}
			return "", err
		}
		return s.resolveTeamID(ctx, parentTable.String, parentID.String, "", seen)
	default:
		return "", nil
	}
}

func shortID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) > 16 {
		return clean[:8] + "-" + clean[len(clean)-8:]
	}
	if clean == "" {
		return "unknown"
	}
	return clean
}

func fallbackSpaceName(id string) string {
	return "External Space " + shortID(id)
}
