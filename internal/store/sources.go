package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) RetireSourcePageBlocks(ctx context.Context, source, pageID string) (int, error) {
	rows, err := s.queryContext(ctx, `select record_sources.record_id
		from record_sources
		join blocks on blocks.id = record_sources.record_id
		where record_sources.record_table = 'block'
			and record_sources.source = ?
			and record_sources.alive = 1
			and (
				(coalesce(record_sources.payload_json, '') <> ''
					and json_valid(record_sources.payload_json)
					and json_extract(record_sources.payload_json, '$.PageID') = ?)
				or
				(coalesce(record_sources.payload_json, '') = ''
					and blocks.source = record_sources.source
					and blocks.page_id = ?)
			)`, source, pageID, pageID)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.retireRecordSource(ctx, "block", id, source, "parent-delete-event"); err != nil {
			return 0, err
		}
	}
	if err := s.markPageFTS(ctx, pageID); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (s *Store) RetireSourcePageBlocksNotSyncedAt(ctx context.Context, source, pageID string, syncedAt int64) (int, error) {
	rows, err := s.queryContext(ctx, `select record_sources.record_id
		from record_sources
		join blocks on blocks.id = record_sources.record_id
		where record_sources.record_table = 'block'
			and record_sources.source = ?
			and record_sources.alive = 1
			and record_sources.synced_at <> ?
			and (
				(coalesce(record_sources.payload_json, '') <> ''
					and json_valid(record_sources.payload_json)
					and json_extract(record_sources.payload_json, '$.PageID') = ?)
				or
				(coalesce(record_sources.payload_json, '') = ''
					and blocks.source = record_sources.source
					and blocks.page_id = ?)
			)`, source, syncedAt, pageID, pageID)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.retireRecordSource(ctx, "block", id, source, "complete-authoritative-enumeration"); err != nil {
			return 0, err
		}
	}
	if err := s.markPageFTS(ctx, pageID); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (s *Store) RetireSourcePageComments(ctx context.Context, source, pageID string) (int64, error) {
	ids, err := s.sourceCommentIDsForPage(ctx, source, pageID)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.retireRecordSource(ctx, "comment", id, source, "parent-delete-event"); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), nil
}

func (s *Store) sourceCommentIDsForPage(ctx context.Context, source, pageID string) ([]string, error) {
	rows, err := s.queryContext(ctx, `select record_sources.record_id
		from record_sources
		join comments on comments.id = record_sources.record_id
		where record_sources.record_table = 'comment'
			and record_sources.source = ?
			and record_sources.alive = 1
			and (
				(coalesce(record_sources.payload_json, '') <> ''
					and json_valid(record_sources.payload_json)
					and json_extract(record_sources.payload_json, '$.PageID') = ?)
				or
				(coalesce(record_sources.payload_json, '') = ''
					and comments.source = record_sources.source
					and comments.page_id = ?)
			)`, source, pageID, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) retireRecordSource(ctx context.Context, recordTable, recordID, source, reason string) error {
	if _, err := s.execContext(ctx, `update record_sources set
		alive = 0,
		payload_json = null,
		deleted_at = ?,
		deletion_source = ?,
		deletion_reason = ?
		where record_table = ? and record_id = ? and source = ?`,
		NowMS(), source, reason, recordTable, recordID, source); err != nil {
		return err
	}
	canonicalSource, _, exists, err := s.canonicalRecordSource(ctx, recordTable, recordID)
	if err != nil {
		return err
	}
	if exists && canonicalSource == source {
		if _, err := s.promoteFallbackRecord(ctx, recordTable, recordID); err != nil {
			return err
		}
	}
	if err := s.refreshRecordAlive(ctx, recordTable, recordID); err != nil {
		return err
	}
	if recordTable == "comment" {
		return s.refreshCommentFTS(ctx, recordID)
	}
	return nil
}

func (s *Store) upsertRecordSource(ctx context.Context, recordTable, recordID, source string, syncedAt int64, alive bool, payload string) error {
	var deletedAt any
	var deletionSource any
	var deletionReason any
	if !alive {
		deletedAt = syncedAt
		deletionSource = source
		deletionReason = "explicit-source-delete"
	}
	_, err := s.execContext(ctx, `insert into record_sources(
		record_table, record_id, source, synced_at, alive, payload_json,
		deleted_at, deletion_source, deletion_reason)
		values (?, ?, ?, ?, ?, nullif(?, ''), ?, ?, ?)
		on conflict(record_table, record_id, source) do update set
			synced_at = excluded.synced_at,
			alive = excluded.alive,
			payload_json = excluded.payload_json,
			deleted_at = excluded.deleted_at,
			deletion_source = excluded.deletion_source,
			deletion_reason = excluded.deletion_reason`,
		recordTable, recordID, source, syncedAt, BoolInt(alive), payload,
		deletedAt, deletionSource, deletionReason)
	return err
}

func sourcePreferred(incoming, current string) bool {
	return sourcePriority(incoming) >= sourcePriority(current)
}

func sourcePriority(source string) int {
	switch source {
	case SourceAPI:
		return 3
	case SourceNotionMCP:
		return 2
	default:
		return 1
	}
}

func (s *Store) preferredFallbackPayload(ctx context.Context, recordTable, recordID, source, incoming string) (string, error) {
	var existing sql.NullString
	err := s.queryRowContext(ctx, `select payload_json from record_sources
		where record_table = ? and record_id = ? and source = ?`, recordTable, recordID, source).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) || !existing.Valid || existing.String == "" {
		return incoming, nil
	}
	if err != nil {
		return "", err
	}
	incoming = preserveFallbackStructure(recordTable, existing.String, incoming)
	if fallbackPayloadPreferred(recordTable, existing.String, incoming) {
		return incoming, nil
	}
	return existing.String, nil
}

func preserveFallbackStructure(recordTable, existing, incoming string) string {
	if recordTable != "block" {
		return incoming
	}
	var oldValue, newValue Block
	if json.Unmarshal([]byte(existing), &oldValue) != nil || json.Unmarshal([]byte(incoming), &newValue) != nil {
		return incoming
	}
	if newValue.PageID != "" || oldValue.PageID == "" {
		return incoming
	}
	newValue.PageID = oldValue.PageID
	if newValue.SpaceID == "" {
		newValue.SpaceID = oldValue.SpaceID
	}
	if newValue.ParentID == "" {
		newValue.ParentID = oldValue.ParentID
	}
	if newValue.ParentTable == "" {
		newValue.ParentTable = oldValue.ParentTable
	}
	if newValue.Type == "" {
		newValue.Type = oldValue.Type
	}
	if newValue.Text == "" {
		newValue.Text = oldValue.Text
	}
	if newValue.PropertiesJSON == "" {
		newValue.PropertiesJSON = oldValue.PropertiesJSON
	}
	if newValue.ContentJSON == "" {
		newValue.ContentJSON = oldValue.ContentJSON
	}
	if newValue.FormatJSON == "" {
		newValue.FormatJSON = oldValue.FormatJSON
	}
	if newValue.RawJSON == "" {
		newValue.RawJSON = oldValue.RawJSON
	}
	payload, err := json.Marshal(newValue)
	if err != nil {
		return incoming
	}
	return string(payload)
}

func fallbackPayloadPreferred(recordTable, existing, incoming string) bool {
	switch recordTable {
	case "page":
		var oldValue, newValue Page
		if json.Unmarshal([]byte(existing), &oldValue) != nil || json.Unmarshal([]byte(incoming), &newValue) != nil {
			return false
		}
		if newValue.LastEditedTime != oldValue.LastEditedTime {
			return newValue.LastEditedTime > oldValue.LastEditedTime
		}
		return pagePayloadQuality(newValue) >= pagePayloadQuality(oldValue)
	case "block":
		var oldValue, newValue Block
		if json.Unmarshal([]byte(existing), &oldValue) != nil || json.Unmarshal([]byte(incoming), &newValue) != nil {
			return false
		}
		if newValue.LastEditedTime != oldValue.LastEditedTime {
			return newValue.LastEditedTime > oldValue.LastEditedTime
		}
		return blockPayloadQuality(newValue) >= blockPayloadQuality(oldValue)
	case "comment":
		var oldValue, newValue Comment
		if json.Unmarshal([]byte(existing), &oldValue) != nil || json.Unmarshal([]byte(incoming), &newValue) != nil {
			return false
		}
		if newValue.LastEditedTime != oldValue.LastEditedTime {
			return newValue.LastEditedTime > oldValue.LastEditedTime
		}
		return len(newValue.Text)+len(newValue.RawJSON) >= len(oldValue.Text)+len(oldValue.RawJSON)
	default:
		return false
	}
}

func pagePayloadQuality(x Page) int {
	return len(x.Title) + len(x.URL) + len(x.Icon) + len(x.Cover) + len(x.PropertiesJSON) + len(x.RawJSON)
}

func blockPayloadQuality(x Block) int {
	return len(x.Text) + len(x.PropertiesJSON) + len(x.ContentJSON) + len(x.FormatJSON) + len(x.RawJSON)
}

func (s *Store) canonicalRecordSource(ctx context.Context, recordTable, recordID string) (string, bool, bool, error) {
	table, err := canonicalTable(recordTable)
	if err != nil {
		return "", false, false, err
	}
	var source string
	var live int
	err = s.queryRowContext(ctx, `select source, exists(
			select 1 from record_sources
			where record_table = ? and record_id = ? and source = `+table+`.source and alive = 1
		)
		from `+table+` where id = ?`, recordTable, recordID, recordID).Scan(&source, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	return source, IntBool(live), err == nil, err
}

func canonicalTable(recordTable string) (string, error) {
	switch recordTable {
	case "page":
		return "pages", nil
	case "block":
		return "blocks", nil
	case "comment":
		return "comments", nil
	default:
		return "", fmt.Errorf("unsupported record table %q", recordTable)
	}
}

func (s *Store) clearRecordSourcePayload(ctx context.Context, recordTable, recordID, source string) error {
	_, err := s.execContext(ctx, `update record_sources set payload_json = null
		where record_table = ? and record_id = ? and source = ?`, recordTable, recordID, source)
	return err
}

func (s *Store) saveCanonicalPayload(ctx context.Context, recordTable, recordID, source string) error {
	var payload []byte
	switch recordTable {
	case "page":
		x, err := s.pageByID(ctx, recordID)
		if err != nil {
			return err
		}
		payload, err = json.Marshal(x)
		if err != nil {
			return err
		}
	case "block":
		x, err := s.blockByID(ctx, recordID)
		if err != nil {
			return err
		}
		payload, err = json.Marshal(x)
		if err != nil {
			return err
		}
	case "comment":
		x, err := s.commentByID(ctx, recordID)
		if err != nil {
			return err
		}
		payload, err = json.Marshal(x)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported record table %q", recordTable)
	}
	_, err := s.execContext(ctx, `update record_sources set payload_json = ?
		where record_table = ? and record_id = ? and source = ?`, string(payload), recordTable, recordID, source)
	return err
}

func (s *Store) promoteFallbackRecord(ctx context.Context, recordTable, recordID string) (bool, error) {
	var source, payload string
	err := s.queryRowContext(ctx, `select source, payload_json
		from record_sources
		where record_table = ? and record_id = ? and alive = 1 and coalesce(payload_json, '') <> ''
		order by case source when 'api' then 0 when 'notion-mcp' then 1 else 2 end, synced_at desc
		limit 1`, recordTable, recordID).Scan(&source, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch recordTable {
	case "page":
		var x Page
		if err := json.Unmarshal([]byte(payload), &x); err != nil {
			return false, err
		}
		x.Source = source
		x.Alive = true
		if err := s.writeCanonicalPage(ctx, x); err != nil {
			return false, err
		}
		if err := s.markPageFTS(ctx, x.ID); err != nil {
			return false, err
		}
	case "block":
		var x Block
		if err := json.Unmarshal([]byte(payload), &x); err != nil {
			return false, err
		}
		current, err := s.blockByID(ctx, recordID)
		if err != nil {
			return false, err
		}
		x.Source = source
		x.Alive = true
		if err := s.writeCanonicalBlock(ctx, x); err != nil {
			return false, err
		}
		if current.PageID != "" && current.PageID != x.PageID {
			if err := s.markPageFTS(ctx, current.PageID); err != nil {
				return false, err
			}
		}
		if x.PageID != "" {
			if err := s.markPageFTS(ctx, x.PageID); err != nil {
				return false, err
			}
		}
	case "comment":
		var x Comment
		if err := json.Unmarshal([]byte(payload), &x); err != nil {
			return false, err
		}
		x.Source = source
		x.Alive = true
		if err := s.writeCanonicalComment(ctx, x); err != nil {
			return false, err
		}
		if err := s.refreshCommentFTS(ctx, x.ID); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported record table %q", recordTable)
	}
	if err := s.clearRecordSourcePayload(ctx, recordTable, recordID, source); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) RecordHasLiveSource(ctx context.Context, recordTable, recordID, source string) (bool, error) {
	_, alive, err := s.RecordSourceState(ctx, recordTable, recordID, source)
	return alive, err
}

func (s *Store) RecordSourceState(ctx context.Context, recordTable, recordID, source string) (bool, bool, error) {
	var exists int
	var alive int
	err := s.queryRowContext(ctx, `select 1, alive from record_sources
		where record_table = ? and record_id = ? and source = ?`,
		recordTable, recordID, source).Scan(&exists, &alive)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return exists != 0, IntBool(alive), nil
}

func (s *Store) refreshRecordAlive(ctx context.Context, recordTable, recordID string) error {
	table, err := canonicalTable(recordTable)
	if err != nil {
		return err
	}
	_, err = s.execContext(ctx, `update `+table+` set alive = exists (
		select 1 from record_sources
		where record_table = ? and record_id = ? and alive = 1
	) where id = ?`, recordTable, recordID, recordID)
	return err
}

func (s *Store) PageForSource(ctx context.Context, pageID, source string) (Page, bool, error) {
	canonicalSource, canonicalLive, exists, err := s.canonicalRecordSource(ctx, "page", pageID)
	if err != nil {
		return Page{}, false, err
	}
	if exists && canonicalLive && canonicalSource == source {
		page, err := s.pageByID(ctx, pageID)
		return page, err == nil, err
	}
	var payload sql.NullString
	err = s.queryRowContext(ctx, `select payload_json from record_sources
		where record_table = 'page' and record_id = ? and source = ? and alive = 1`,
		pageID, source).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) || !payload.Valid || strings.TrimSpace(payload.String) == "" {
		return Page{}, false, nil
	}
	if err != nil {
		return Page{}, false, err
	}
	var page Page
	if err := json.Unmarshal([]byte(payload.String), &page); err != nil {
		return Page{}, false, err
	}
	return page, true, nil
}

func (s *Store) NextSourceSyncAt(ctx context.Context, recordTable, source string) (int64, error) {
	var latest sql.NullInt64
	if err := s.queryRowContext(ctx, `select max(synced_at) from record_sources
		where record_table = ? and source = ?`, recordTable, source).Scan(&latest); err != nil {
		return 0, err
	}
	now := NowMS()
	if latest.Valid && latest.Int64 >= now {
		return latest.Int64 + 1, nil
	}
	return now, nil
}
