package store

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Store) UpsertSpace(ctx context.Context, x Space) error {
	_, err := s.execContext(ctx, `insert into spaces(id, name, raw_json, source, synced_at)
		values (?, ?, ?, ?, ?)
		on conflict(id) do update set name=excluded.name, raw_json=excluded.raw_json, source=excluded.source, synced_at=excluded.synced_at`,
		x.ID, x.Name, x.RawJSON, x.Source, x.SyncedAt)
	return err
}

func (s *Store) EnsureSpaceFallbacks(ctx context.Context, source string) (int, error) {
	rows, err := s.queryContext(ctx, `select distinct space_id from (
			select space_id from pages
			union all select space_id from blocks
			union all select space_id from teams
			union all select space_id from collections
			union all select space_id from comments
			union all select space_id from raw_records
		)
		where coalesce(space_id, '') <> ''
			and space_id not in (select id from spaces)`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := NowMS()
	for _, id := range ids {
		if err := s.UpsertSpace(ctx, Space{
			ID:       id,
			Name:     fallbackSpaceName(id),
			RawJSON:  fmt.Sprintf(`{"id":%q,"inferred":true}`, id),
			Source:   source,
			SyncedAt: now,
		}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *Store) UpsertUser(ctx context.Context, x User) error {
	_, err := s.execContext(ctx, `insert into users(id, name, email, raw_json, source, synced_at)
		values (?, ?, ?, ?, ?, ?)
		on conflict(id) do update set name=excluded.name, email=excluded.email, raw_json=excluded.raw_json, source=excluded.source, synced_at=excluded.synced_at`,
		x.ID, x.Name, x.Email, x.RawJSON, x.Source, x.SyncedAt)
	return err
}

func (s *Store) UpsertTeam(ctx context.Context, x Team) error {
	_, err := s.execContext(ctx, `insert into teams(id, space_id, parent_id, parent_table, name, raw_json, source, synced_at)
		values (?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			space_id=excluded.space_id,
			parent_id=excluded.parent_id,
			parent_table=excluded.parent_table,
			name=excluded.name,
			raw_json=excluded.raw_json,
			source=excluded.source,
			synced_at=excluded.synced_at`,
		x.ID, x.SpaceID, x.ParentID, x.ParentTable, x.Name, x.RawJSON, x.Source, x.SyncedAt)
	return err
}

func (s *Store) UpsertPage(ctx context.Context, x Page) error {
	payload, err := json.Marshal(x)
	if err != nil {
		return err
	}
	canonicalSource, canonicalLive, exists, err := s.canonicalRecordSource(ctx, "page", x.ID)
	if err != nil {
		return err
	}
	sourcePayload := string(payload)
	if !x.Alive {
		sourcePayload = ""
	}
	if x.Alive && exists && canonicalSource != x.Source && canonicalLive && !sourcePreferred(x.Source, canonicalSource) {
		sourcePayload, err = s.preferredFallbackPayload(ctx, "page", x.ID, x.Source, sourcePayload)
		if err != nil {
			return err
		}
	}
	if err := s.upsertRecordSource(ctx, "page", x.ID, x.Source, x.SyncedAt, x.Alive, sourcePayload); err != nil {
		return err
	}
	if !exists {
		if err := s.writeCanonicalPage(ctx, x); err != nil {
			return err
		}
		if err := s.clearRecordSourcePayload(ctx, "page", x.ID, x.Source); err != nil {
			return err
		}
		if err := s.refreshRecordAlive(ctx, "page", x.ID); err != nil {
			return err
		}
		return s.markPageFTS(ctx, x.ID)
	}
	if !x.Alive {
		if canonicalSource == x.Source {
			if _, err := s.promoteFallbackRecord(ctx, "page", x.ID); err != nil {
				return err
			}
		}
		if err := s.refreshRecordAlive(ctx, "page", x.ID); err != nil {
			return err
		}
		return s.markPageFTS(ctx, x.ID)
	}
	if canonicalSource != x.Source && canonicalLive && !sourcePreferred(x.Source, canonicalSource) {
		return s.refreshRecordAlive(ctx, "page", x.ID)
	}
	if canonicalSource != x.Source && canonicalLive {
		if err := s.saveCanonicalPayload(ctx, "page", x.ID, canonicalSource); err != nil {
			return err
		}
	}
	if err := s.writeCanonicalPage(ctx, x); err != nil {
		return err
	}
	if err := s.clearRecordSourcePayload(ctx, "page", x.ID, x.Source); err != nil {
		return err
	}
	if err := s.refreshRecordAlive(ctx, "page", x.ID); err != nil {
		return err
	}
	return s.markPageFTS(ctx, x.ID)
}

func (s *Store) writeCanonicalPage(ctx context.Context, x Page) error {
	_, err := s.execContext(ctx, `insert into pages(
		id, space_id, parent_id, parent_table, collection_id, title, url, icon, cover, properties_json,
		created_time, last_edited_time, alive, source, raw_json, synced_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			space_id=excluded.space_id,
			parent_id=excluded.parent_id,
			parent_table=excluded.parent_table,
			collection_id=excluded.collection_id,
			title=excluded.title,
			url=excluded.url,
			icon=excluded.icon,
			cover=excluded.cover,
			properties_json=excluded.properties_json,
			created_time=excluded.created_time,
			last_edited_time=excluded.last_edited_time,
			alive=excluded.alive,
			source=excluded.source,
			raw_json=excluded.raw_json,
			synced_at=excluded.synced_at`,
		x.ID, x.SpaceID, x.ParentID, x.ParentTable, x.CollectionID, x.Title, x.URL, x.Icon, x.Cover, x.PropertiesJSON,
		x.CreatedTime, x.LastEditedTime, BoolInt(x.Alive), x.Source, x.RawJSON, x.SyncedAt)
	return err
}

func (s *Store) UpsertBlock(ctx context.Context, x Block) error {
	payload, err := json.Marshal(x)
	if err != nil {
		return err
	}
	canonicalSource, canonicalLive, exists, err := s.canonicalRecordSource(ctx, "block", x.ID)
	if err != nil {
		return err
	}
	sourcePayload := string(payload)
	if !x.Alive {
		sourcePayload = ""
	}
	if x.Alive && exists && canonicalSource != x.Source && canonicalLive && !sourcePreferred(x.Source, canonicalSource) {
		sourcePayload, err = s.preferredFallbackPayload(ctx, "block", x.ID, x.Source, sourcePayload)
		if err != nil {
			return err
		}
	}
	if err := s.upsertRecordSource(ctx, "block", x.ID, x.Source, x.SyncedAt, x.Alive, sourcePayload); err != nil {
		return err
	}
	if !exists {
		if err := s.writeCanonicalBlock(ctx, x); err != nil {
			return err
		}
		if err := s.clearRecordSourcePayload(ctx, "block", x.ID, x.Source); err != nil {
			return err
		}
		if err := s.refreshRecordAlive(ctx, "block", x.ID); err != nil {
			return err
		}
		if x.PageID != "" {
			return s.markPageFTS(ctx, x.PageID)
		}
		return nil
	}
	if !x.Alive {
		if canonicalSource == x.Source {
			if _, err := s.promoteFallbackRecord(ctx, "block", x.ID); err != nil {
				return err
			}
		}
		if err := s.refreshRecordAlive(ctx, "block", x.ID); err != nil {
			return err
		}
		if x.PageID != "" {
			return s.markPageFTS(ctx, x.PageID)
		}
		return nil
	}
	if canonicalSource != x.Source && canonicalLive && !sourcePreferred(x.Source, canonicalSource) {
		return s.refreshRecordAlive(ctx, "block", x.ID)
	}
	if canonicalSource != x.Source && canonicalLive {
		if err := s.saveCanonicalPayload(ctx, "block", x.ID, canonicalSource); err != nil {
			return err
		}
	}
	current, err := s.blockByID(ctx, x.ID)
	if err != nil {
		return err
	}
	if err := s.writeCanonicalBlock(ctx, x); err != nil {
		return err
	}
	if err := s.clearRecordSourcePayload(ctx, "block", x.ID, x.Source); err != nil {
		return err
	}
	if err := s.refreshRecordAlive(ctx, "block", x.ID); err != nil {
		return err
	}
	if current.PageID != "" && current.PageID != x.PageID {
		if err := s.markPageFTS(ctx, current.PageID); err != nil {
			return err
		}
	}
	if x.PageID != "" {
		return s.markPageFTS(ctx, x.PageID)
	}
	return nil
}

func (s *Store) writeCanonicalBlock(ctx context.Context, x Block) error {
	_, err := s.execContext(ctx, `insert into blocks(
		id, page_id, space_id, parent_id, parent_table, type, text, properties_json, content_json, format_json,
		display_order, created_time, last_edited_time, alive, source, raw_json, synced_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			page_id=excluded.page_id,
			space_id=excluded.space_id,
			parent_id=excluded.parent_id,
			parent_table=excluded.parent_table,
			type=excluded.type,
			text=excluded.text,
			properties_json=excluded.properties_json,
			content_json=excluded.content_json,
			format_json=excluded.format_json,
			display_order=excluded.display_order,
			created_time=excluded.created_time,
			last_edited_time=excluded.last_edited_time,
			alive=excluded.alive,
			source=excluded.source,
			raw_json=excluded.raw_json,
			synced_at=excluded.synced_at`,
		x.ID, x.PageID, x.SpaceID, x.ParentID, x.ParentTable, x.Type, x.Text, x.PropertiesJSON, x.ContentJSON, x.FormatJSON,
		x.DisplayOrder, x.CreatedTime, x.LastEditedTime, BoolInt(x.Alive), x.Source, x.RawJSON, x.SyncedAt)
	return err
}

func (s *Store) UpsertCollection(ctx context.Context, x Collection) error {
	_, err := s.execContext(ctx, `insert into collections(id, space_id, parent_id, parent_table, name, schema_json, format_json, raw_json, source, synced_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set space_id=excluded.space_id, parent_id=excluded.parent_id, parent_table=excluded.parent_table, name=excluded.name,
			schema_json=excluded.schema_json, format_json=excluded.format_json, raw_json=excluded.raw_json,
			source=excluded.source, synced_at=excluded.synced_at`,
		x.ID, x.SpaceID, x.ParentID, x.ParentTable, x.Name, x.SchemaJSON, x.FormatJSON, x.RawJSON, x.Source, x.SyncedAt)
	return err
}

func (s *Store) UpsertComment(ctx context.Context, x Comment) error {
	payload, err := json.Marshal(x)
	if err != nil {
		return err
	}
	canonicalSource, canonicalLive, exists, err := s.canonicalRecordSource(ctx, "comment", x.ID)
	if err != nil {
		return err
	}
	sourcePayload := string(payload)
	if !x.Alive {
		sourcePayload = ""
	}
	if x.Alive && exists && canonicalSource != x.Source && canonicalLive && !sourcePreferred(x.Source, canonicalSource) {
		sourcePayload, err = s.preferredFallbackPayload(ctx, "comment", x.ID, x.Source, sourcePayload)
		if err != nil {
			return err
		}
	}
	if err := s.upsertRecordSource(ctx, "comment", x.ID, x.Source, x.SyncedAt, x.Alive, sourcePayload); err != nil {
		return err
	}
	if !exists {
		if err := s.writeCanonicalComment(ctx, x); err != nil {
			return err
		}
		if err := s.clearRecordSourcePayload(ctx, "comment", x.ID, x.Source); err != nil {
			return err
		}
		if err := s.refreshRecordAlive(ctx, "comment", x.ID); err != nil {
			return err
		}
		return s.refreshCommentFTS(ctx, x.ID)
	}
	if !x.Alive {
		if canonicalSource == x.Source {
			if _, err := s.promoteFallbackRecord(ctx, "comment", x.ID); err != nil {
				return err
			}
		}
		if err := s.refreshRecordAlive(ctx, "comment", x.ID); err != nil {
			return err
		}
		return s.refreshCommentFTS(ctx, x.ID)
	}
	if canonicalSource != x.Source && canonicalLive && !sourcePreferred(x.Source, canonicalSource) {
		return s.refreshRecordAlive(ctx, "comment", x.ID)
	}
	if canonicalSource != x.Source && canonicalLive {
		if err := s.saveCanonicalPayload(ctx, "comment", x.ID, canonicalSource); err != nil {
			return err
		}
	}
	if err := s.writeCanonicalComment(ctx, x); err != nil {
		return err
	}
	if err := s.clearRecordSourcePayload(ctx, "comment", x.ID, x.Source); err != nil {
		return err
	}
	if err := s.refreshRecordAlive(ctx, "comment", x.ID); err != nil {
		return err
	}
	return s.refreshCommentFTS(ctx, x.ID)
}

func (s *Store) writeCanonicalComment(ctx context.Context, x Comment) error {
	_, err := s.execContext(ctx, `insert into comments(id, page_id, space_id, parent_id, text, created_by_id, created_time, last_edited_time, alive, raw_json, source, synced_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set page_id=excluded.page_id, space_id=excluded.space_id, parent_id=excluded.parent_id,
			text=excluded.text, created_by_id=excluded.created_by_id, created_time=excluded.created_time,
			last_edited_time=excluded.last_edited_time, alive=excluded.alive, raw_json=excluded.raw_json,
			source=excluded.source, synced_at=excluded.synced_at`,
		x.ID, x.PageID, x.SpaceID, x.ParentID, x.Text, x.CreatedByID, x.CreatedTime, x.LastEditedTime, BoolInt(x.Alive), x.RawJSON, x.Source, x.SyncedAt)
	return err
}

func (s *Store) UpsertRawRecord(ctx context.Context, x RawRecord) error {
	_, err := s.execContext(ctx, `insert into raw_records(source, record_table, record_id, parent_id, space_id, raw_json, synced_at)
		values (?, ?, ?, ?, ?, ?, ?)
		on conflict(source, record_table, record_id) do update set parent_id=excluded.parent_id, space_id=excluded.space_id,
			raw_json=excluded.raw_json, synced_at=excluded.synced_at`,
		x.Source, x.RecordTable, x.RecordID, x.ParentID, x.SpaceID, x.RawJSON, x.SyncedAt)
	return err
}
