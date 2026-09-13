package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) refreshCommentFTS(ctx context.Context, commentID string) error {
	if _, err := s.execContext(ctx, `delete from comment_fts where comment_id = ?`, commentID); err != nil {
		return err
	}
	var pageID, text string
	err := s.queryRowContext(ctx, `select page_id, text from comments where id = ? and alive = 1`, commentID).Scan(&pageID, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.execContext(ctx, `insert into comment_fts(comment_id, page_id, body) values (?, ?, ?)`, commentID, pageID, text)
	return err
}

func (s *Store) DeferPageFTS(ctx context.Context, fn func() error) error {
	outer := s.deferredFTS == 0
	if outer {
		s.deferredFTSPages = map[string]bool{}
	}
	s.deferredFTS++
	err := fn()
	s.deferredFTS--
	if !outer {
		return err
	}
	pages := s.deferredFTSPages
	s.deferredFTSPages = nil
	if err != nil {
		return err
	}
	for pageID := range pages {
		if err := s.refreshPageFTS(ctx, pageID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) markPageFTS(ctx context.Context, pageID string) error {
	if pageID == "" {
		return nil
	}
	if s.deferredFTS > 0 {
		if s.deferredFTSPages == nil {
			s.deferredFTSPages = map[string]bool{}
		}
		s.deferredFTSPages[pageID] = true
		return nil
	}
	return s.refreshPageFTS(ctx, pageID)
}

func (s *Store) refreshPageFTS(ctx context.Context, pageID string) error {
	if _, err := s.execContext(ctx, `delete from page_fts where page_id = ?`, pageID); err != nil {
		return err
	}
	var title string
	if err := s.queryRowContext(ctx, `select title from pages where id = ? and alive = 1`, pageID).Scan(&title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	blocks, err := s.PageBlocks(ctx, pageID)
	if err != nil {
		return err
	}
	apiBlocksSynced, err := s.HasSyncState(ctx, SourceAPI, "page_blocks", pageID)
	if err != nil {
		return err
	}
	parts := pageBlockTextParts(pageID, blocks, apiBlocksSynced)
	_, err = s.execContext(ctx, `insert into page_fts(page_id, title, body) values (?, ?, ?)`, pageID, title, strings.Join(parts, "\n"))
	return err
}

func pageBlockTextParts(pageID string, blocks []Block, apiBlocksSynced bool) []string {
	blocks = PreferredPageContentBlocks(blocks, apiBlocksSynced)
	children := map[string][]Block{}
	for _, block := range blocks {
		if block.ID == pageID {
			continue
		}
		children[block.ParentID] = append(children[block.ParentID], block)
	}
	for parent := range children {
		SortBlockSiblings(children[parent])
	}

	var parts []string
	var appendChildren func(string)
	appendChildren = func(parentID string) {
		for _, block := range children[parentID] {
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
			appendChildren(block.ID)
		}
	}
	appendChildren(pageID)
	if len(children[pageID]) == 0 {
		for _, block := range blocks {
			if block.ID == pageID || block.ParentID == pageID {
				continue
			}
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
	}
	return parts
}

func (s *Store) Search(ctx context.Context, q string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.queryContext(ctx, `select kind, id, title, text from (
			select 'page' as kind,
				page_fts.page_id as id,
				page_fts.title as title,
				snippet(page_fts, 2, '[', ']', '...', 16) as text,
				bm25(page_fts) as rank,
				coalesce(p.last_edited_time, p.created_time, 0) as edited_at
			from page_fts
			join pages p on p.id = page_fts.page_id
			where page_fts match ?
			union all
			select 'comment' as kind,
				comment_fts.comment_id as id,
				coalesce(p.title, '') as title,
				snippet(comment_fts, 2, '[', ']', '...', 16) as text,
				bm25(comment_fts) as rank,
				coalesce(c.last_edited_time, c.created_time, 0) as edited_at
			from comment_fts
			join comments c on c.id = comment_fts.comment_id
			left join pages p on p.id = comment_fts.page_id
			where comment_fts match ?
		)
		order by rank, edited_at desc, kind, lower(title), id
		limit ?`, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Kind, &r.ID, &r.Title, &r.Text); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RebuildFTS(ctx context.Context) error {
	if _, err := s.execContext(ctx, `delete from page_fts`); err != nil {
		return err
	}
	rows, err := s.queryContext(ctx, `select id from pages`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.refreshPageFTS(ctx, id); err != nil {
			return err
		}
	}
	if _, err := s.execContext(ctx, `delete from comment_fts`); err != nil {
		return err
	}
	_, err = s.execContext(ctx, `insert into comment_fts(comment_id, page_id, body) select id, page_id, text from comments where alive = 1`)
	return err
}
