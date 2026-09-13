package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const schemaVersion = 4

func (s *Store) init(ctx context.Context) error {
	stmts := []string{
		`pragma foreign_keys = on`,
		`pragma journal_mode = wal`,
		`pragma synchronous = normal`,
		`pragma temp_store = memory`,
		`pragma mmap_size = 268435456`,
		`pragma busy_timeout = 5000`,
		`create table if not exists meta (key text primary key, value text not null)`,
		`create table if not exists spaces (
			id text primary key,
			name text not null,
			raw_json text,
			source text not null,
			synced_at integer not null
		)`,
		`create table if not exists users (
			id text primary key,
			name text,
			email text,
			raw_json text,
			source text not null,
			synced_at integer not null
		)`,
		`create table if not exists teams (
			id text primary key,
			space_id text,
			parent_id text,
			parent_table text,
			name text not null,
			raw_json text,
			source text not null,
			synced_at integer not null
		)`,
		`create index if not exists teams_space_id on teams(space_id)`,
		`create table if not exists pages (
			id text primary key,
			space_id text,
			parent_id text,
			parent_table text,
			collection_id text,
			title text not null,
			url text,
			icon text,
			cover text,
			properties_json text,
			created_time integer,
			last_edited_time integer,
			alive integer not null,
			source text not null,
			raw_json text,
			synced_at integer not null
		)`,
		`create index if not exists pages_collection_id on pages(collection_id)`,
		`create index if not exists pages_parent_id on pages(parent_id)`,
		`create index if not exists pages_last_edited_time on pages(last_edited_time desc)`,
		`create index if not exists pages_source_synced_at on pages(source, synced_at desc)`,
		`create table if not exists blocks (
			id text primary key,
			page_id text,
			space_id text,
			parent_id text,
			parent_table text,
			type text not null,
			text text,
			properties_json text,
			content_json text,
			format_json text,
			display_order integer not null default 0,
			created_time integer,
			last_edited_time integer,
			alive integer not null,
			source text not null,
			raw_json text,
			synced_at integer not null
		)`,
		`create index if not exists blocks_page_id on blocks(page_id)`,
		`create index if not exists blocks_parent_id on blocks(parent_id)`,
		`create table if not exists collections (
			id text primary key,
			space_id text,
			parent_id text,
			parent_table text,
			name text,
			schema_json text,
			format_json text,
			raw_json text,
			source text not null,
			synced_at integer not null
		)`,
		`create index if not exists collections_parent_id on collections(parent_id)`,
		`create index if not exists collections_name on collections(name)`,
		`create table if not exists comments (
			id text primary key,
			page_id text,
			space_id text,
			parent_id text,
			text text,
			created_by_id text,
			created_time integer,
			last_edited_time integer,
			alive integer not null,
			raw_json text,
			source text not null,
			synced_at integer not null
		)`,
		`create index if not exists comments_page_id on comments(page_id)`,
		`create index if not exists comments_created_time on comments(created_time, id)`,
		`create table if not exists raw_records (
			source text not null,
			record_table text not null,
			record_id text not null,
			parent_id text,
			space_id text,
			raw_json text not null,
			synced_at integer not null,
			primary key (source, record_table, record_id)
		)`,
		`create index if not exists raw_records_parent on raw_records(parent_id, record_table)`,
		`create table if not exists record_sources (
			record_table text not null,
			record_id text not null,
			source text not null,
			synced_at integer not null,
			alive integer not null,
			payload_json text,
			deleted_at integer,
			deletion_source text,
			deletion_reason text,
			primary key (record_table, record_id, source)
		)`,
		`create index if not exists record_sources_source_sync on record_sources(record_table, source, alive, synced_at)`,
		`create table if not exists record_revisions (
			id integer primary key autoincrement,
			record_table text not null,
			record_key text not null,
			payload_json text not null,
			recorded_at integer not null,
			event_source text not null,
			reason text not null
		)`,
		`create index if not exists record_revisions_record on record_revisions(record_table, record_key, recorded_at desc)`,
		`create table if not exists sync_state (
			source text not null,
			entity_type text not null,
			entity_id text not null,
			cursor text,
			synced_at integer not null,
			primary key (source, entity_type, entity_id)
		)`,
		`create index if not exists sync_state_synced_at on sync_state(synced_at desc)`,
		`create virtual table if not exists page_fts using fts5(page_id unindexed, title, body)`,
		`create virtual table if not exists comment_fts using fts5(comment_id unindexed, page_id unindexed, body)`,
	}
	for _, stmt := range stmts {
		if _, err := s.execContext(ctx, stmt); err != nil {
			return err
		}
	}
	var current int
	row := s.queryRowContext(ctx, `select value from meta where key = 'schema_version'`)
	err := row.Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than this notcrawl build supports (%d)", current, schemaVersion)
	}
	if err := s.ensureColumn(ctx, "blocks", "display_order", "integer not null default 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "collections", "parent_table", "text"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "record_sources", "payload_json", "text"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "record_sources", "deleted_at", "integer"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "record_sources", "deletion_source", "text"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "record_sources", "deletion_reason", "text"); err != nil {
		return err
	}
	if _, err := s.execContext(ctx, `create index if not exists blocks_page_alive_order on blocks(page_id, alive, parent_id, display_order, created_time, id)`); err != nil {
		return err
	}
	if _, err := s.execContext(ctx, `create index if not exists blocks_page_alive_created on blocks(page_id, alive, created_time, id)`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`insert or ignore into record_sources(record_table, record_id, source, synced_at, alive)
			select 'page', id, source, synced_at, alive from pages`,
		`insert or ignore into record_sources(record_table, record_id, source, synced_at, alive)
			select 'block', id, source, synced_at, alive from blocks`,
		`insert or ignore into record_sources(record_table, record_id, source, synced_at, alive)
			select 'comment', id, source, synced_at, alive from comments`,
	} {
		if _, err := s.execContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := s.execContext(ctx, `update record_sources set
		deleted_at = coalesce(deleted_at, synced_at),
		deletion_source = coalesce(deletion_source, source),
		deletion_reason = coalesce(deletion_reason, 'legacy-tombstone')
		where alive = 0`); err != nil {
		return err
	}
	if _, err := s.execContext(ctx, `insert or replace into meta(key, value) values('schema_version', ?)`, schemaVersion); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.queryContext(ctx, `pragma table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.execContext(ctx, `alter table `+table+` add column `+column+` `+definition)
	return err
}
