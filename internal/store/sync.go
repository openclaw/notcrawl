package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) SetSyncState(ctx context.Context, source, entityType, entityID, cursor string) error {
	if _, err := s.execContext(ctx, `insert into sync_state(source, entity_type, entity_id, cursor, synced_at)
		values (?, ?, ?, ?, ?)
		on conflict(source, entity_type, entity_id) do update set cursor=excluded.cursor, synced_at=excluded.synced_at`,
		source, entityType, entityID, cursor, NowMS()); err != nil {
		return err
	}
	if entityType == "page_blocks" {
		return s.markPageFTS(ctx, entityID)
	}
	return nil
}

func (s *Store) ClearSyncState(ctx context.Context, source, entityType, entityID string) error {
	if _, err := s.execContext(ctx, `delete from sync_state
		where source = ? and entity_type = ? and entity_id = ?`, source, entityType, entityID); err != nil {
		return err
	}
	if entityType == "page_blocks" {
		return s.markPageFTS(ctx, entityID)
	}
	return nil
}

func (s *Store) HasSyncState(ctx context.Context, source, entityType, entityID string) (bool, error) {
	var exists int
	err := s.queryRowContext(ctx, `select exists(
		select 1 from sync_state
		where source = ? and entity_type = ? and entity_id = ?
	)`, source, entityType, entityID).Scan(&exists)
	return exists != 0, err
}

func (s *Store) SyncStateCursor(ctx context.Context, source, entityType, entityID string) (string, bool, error) {
	var cursor sql.NullString
	err := s.queryRowContext(ctx, `select cursor from sync_state
		where source = ? and entity_type = ? and entity_id = ?`,
		source, entityType, entityID).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return cursor.String, true, nil
}
