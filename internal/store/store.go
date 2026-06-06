package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"
)

const schemaVersion = 3

type Store struct {
	db               *sql.DB
	tx               *sql.Tx
	path             string
	deferredFTS      int
	deferredFTSPages map[string]bool
}

func Open(path string) (*Store, error) {
	base, err := crawlstore.Open(context.Background(), crawlstore.Options{Path: path})
	if err != nil {
		return nil, err
	}
	db := base.DB()
	if err := db.PingContext(context.Background()); err != nil {
		_ = base.Close()
		return nil, err
	}
	st := &Store{db: db, path: path}
	if err := st.init(context.Background()); err != nil {
		_ = base.Close()
		return nil, err
	}
	return st, nil
}

func OpenReadOnly(path string) (*Store, error) {
	base, err := crawlstore.OpenReadOnly(context.Background(), path)
	if err != nil {
		return nil, err
	}
	db := base.DB()
	if err := db.PingContext(context.Background()); err != nil {
		_ = base.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.tx != nil {
		return s.tx.ExecContext(ctx, query, args...)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s.tx != nil {
		return s.tx.QueryContext(ctx, query, args...)
	}
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if s.tx != nil {
		return s.tx.QueryRowContext(ctx, query, args...)
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

func (s *Store) WithTransaction(ctx context.Context, fn func() error) error {
	if s.tx != nil {
		return fn()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	s.tx = tx
	err = fn()
	s.tx = nil
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

type Status struct {
	DBPath      string `json:"db_path"`
	DBBytes     int64  `json:"db_bytes"`
	WALBytes    int64  `json:"wal_bytes"`
	Spaces      int    `json:"spaces"`
	Users       int    `json:"users"`
	Teams       int    `json:"teams"`
	Pages       int    `json:"pages"`
	Blocks      int    `json:"blocks"`
	Collections int    `json:"collections"`
	Comments    int    `json:"comments"`
	RawRecords  int    `json:"raw_records"`
	LastSyncAt  int64  `json:"last_sync_at"`
}

type MaintenanceSummary struct {
	RebuiltFTS bool   `json:"rebuilt_fts"`
	Optimized  bool   `json:"optimized"`
	Analyzed   bool   `json:"analyzed"`
	Vacuumed   bool   `json:"vacuumed"`
	DBBytes    int64  `json:"db_bytes"`
	WALBytes   int64  `json:"wal_bytes"`
	Message    string `json:"message"`
}

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
			primary key (record_table, record_id, source)
		)`,
		`create index if not exists record_sources_source_sync on record_sources(record_table, source, alive, synced_at)`,
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

func (s *Store) RebuildRecordSources(ctx context.Context) error {
	if _, err := s.execContext(ctx, `delete from record_sources`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`insert into record_sources(record_table, record_id, source, synced_at, alive)
			select 'page', id, source, synced_at, alive from pages`,
		`insert into record_sources(record_table, record_id, source, synced_at, alive)
			select 'block', id, source, synced_at, alive from blocks`,
		`insert into record_sources(record_table, record_id, source, synced_at, alive)
			select 'comment', id, source, synced_at, alive from comments`,
	} {
		if _, err := s.execContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func NowMS() int64 {
	return time.Now().UnixMilli()
}

func BoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func IntBool(v int) bool {
	return v != 0
}

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
		if _, err := s.execContext(ctx, `update record_sources set alive = 0, payload_json = null
			where record_table = 'block' and record_id = ? and source = ?`, id, source); err != nil {
			return 0, err
		}
		canonicalSource, _, exists, err := s.canonicalRecordSource(ctx, "block", id)
		if err != nil {
			return 0, err
		}
		if exists && canonicalSource == source {
			if _, err := s.promoteFallbackRecord(ctx, "block", id); err != nil {
				return 0, err
			}
		}
		if err := s.refreshRecordAlive(ctx, "block", id); err != nil {
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
		if err := s.retireRecordSource(ctx, "block", id, source); err != nil {
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
		if err := s.retireRecordSource(ctx, "comment", id, source); err != nil {
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

func (s *Store) retireRecordSource(ctx context.Context, recordTable, recordID, source string) error {
	if _, err := s.execContext(ctx, `update record_sources set alive = 0, payload_json = null
		where record_table = ? and record_id = ? and source = ?`, recordTable, recordID, source); err != nil {
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
	_, err := s.execContext(ctx, `insert into record_sources(record_table, record_id, source, synced_at, alive, payload_json)
		values (?, ?, ?, ?, ?, nullif(?, ''))
		on conflict(record_table, record_id, source) do update set
			synced_at = excluded.synced_at,
			alive = excluded.alive,
			payload_json = excluded.payload_json`,
		recordTable, recordID, source, syncedAt, BoolInt(alive), payload)
	return err
}

func sourcePreferred(incoming, current string) bool {
	if incoming == "api" {
		return current != "api"
	}
	return current != "api"
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
		order by case when source = 'api' then 0 else 1 end, synced_at desc
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

func (s *Store) pageByID(ctx context.Context, id string) (Page, error) {
	var x Page
	var alive int
	err := s.queryRowContext(ctx, `select id, space_id, parent_id, parent_table, collection_id, title, url, icon, cover,
		properties_json, created_time, last_edited_time, alive, source, raw_json, synced_at
		from pages where id = ?`, id).Scan(
		&x.ID, &x.SpaceID, &x.ParentID, &x.ParentTable, &x.CollectionID, &x.Title, &x.URL, &x.Icon, &x.Cover,
		&x.PropertiesJSON, &x.CreatedTime, &x.LastEditedTime, &alive, &x.Source, &x.RawJSON, &x.SyncedAt)
	x.Alive = IntBool(alive)
	return x, err
}

func (s *Store) blockByID(ctx context.Context, id string) (Block, error) {
	var x Block
	var alive int
	err := s.queryRowContext(ctx, `select id, page_id, space_id, parent_id, parent_table, type, text, properties_json,
		content_json, format_json, display_order, created_time, last_edited_time, alive, source, raw_json, synced_at
		from blocks where id = ?`, id).Scan(
		&x.ID, &x.PageID, &x.SpaceID, &x.ParentID, &x.ParentTable, &x.Type, &x.Text, &x.PropertiesJSON,
		&x.ContentJSON, &x.FormatJSON, &x.DisplayOrder, &x.CreatedTime, &x.LastEditedTime, &alive, &x.Source, &x.RawJSON, &x.SyncedAt)
	x.Alive = IntBool(alive)
	return x, err
}

func (s *Store) commentByID(ctx context.Context, id string) (Comment, error) {
	var x Comment
	var alive int
	err := s.queryRowContext(ctx, `select id, page_id, space_id, parent_id, text, created_by_id,
		created_time, last_edited_time, alive, raw_json, source, synced_at
		from comments where id = ?`, id).Scan(
		&x.ID, &x.PageID, &x.SpaceID, &x.ParentID, &x.Text, &x.CreatedByID,
		&x.CreatedTime, &x.LastEditedTime, &alive, &x.RawJSON, &x.Source, &x.SyncedAt)
	x.Alive = IntBool(alive)
	return x, err
}

func (s *Store) RecordHasLiveSource(ctx context.Context, recordTable, recordID, source string) (bool, error) {
	var exists int
	err := s.queryRowContext(ctx, `select exists(
		select 1 from record_sources
		where record_table = ? and record_id = ? and source = ? and alive = 1
	)`, recordTable, recordID, source).Scan(&exists)
	return exists != 0, err
}

func (s *Store) refreshRecordAlive(ctx context.Context, recordTable, recordID string) error {
	table := ""
	switch recordTable {
	case "page":
		table = "pages"
	case "block":
		table = "blocks"
	case "comment":
		table = "comments"
	default:
		return fmt.Errorf("unsupported record table %q", recordTable)
	}
	_, err := s.execContext(ctx, `update `+table+` set alive = exists (
		select 1 from record_sources
		where record_table = ? and record_id = ? and alive = 1
	) where id = ?`, recordTable, recordID, recordID)
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

func (s *Store) UpsertRawRecord(ctx context.Context, x RawRecord) error {
	_, err := s.execContext(ctx, `insert into raw_records(source, record_table, record_id, parent_id, space_id, raw_json, synced_at)
		values (?, ?, ?, ?, ?, ?, ?)
		on conflict(source, record_table, record_id) do update set parent_id=excluded.parent_id, space_id=excluded.space_id,
			raw_json=excluded.raw_json, synced_at=excluded.synced_at`,
		x.Source, x.RecordTable, x.RecordID, x.ParentID, x.SpaceID, x.RawJSON, x.SyncedAt)
	return err
}

func (s *Store) SetSyncState(ctx context.Context, source, entityType, entityID, cursor string) error {
	_, err := s.execContext(ctx, `insert into sync_state(source, entity_type, entity_id, cursor, synced_at)
		values (?, ?, ?, ?, ?)
		on conflict(source, entity_type, entity_id) do update set cursor=excluded.cursor, synced_at=excluded.synced_at`,
		source, entityType, entityID, cursor, NowMS())
	return err
}

func (s *Store) ClearSyncState(ctx context.Context, source, entityType, entityID string) error {
	_, err := s.execContext(ctx, `delete from sync_state
		where source = ? and entity_type = ? and entity_id = ?`, source, entityType, entityID)
	return err
}

func (s *Store) HasSyncState(ctx context.Context, source, entityType, entityID string) (bool, error) {
	var exists int
	err := s.queryRowContext(ctx, `select exists(
		select 1 from sync_state
		where source = ? and entity_type = ? and entity_id = ?
	)`, source, entityType, entityID).Scan(&exists)
	return exists != 0, err
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
	parts := pageBlockTextParts(pageID, blocks)
	_, err = s.execContext(ctx, `insert into page_fts(page_id, title, body) values (?, ?, ?)`, pageID, title, strings.Join(parts, "\n"))
	return err
}

func pageBlockTextParts(pageID string, blocks []Block) []string {
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

func sortBlockSiblings(blocks []Block) {
	sort.SliceStable(blocks, func(i, j int) bool {
		a, z := blocks[i], blocks[j]
		if a.DisplayOrder != z.DisplayOrder {
			return a.DisplayOrder < z.DisplayOrder
		}
		if a.CreatedTime == z.CreatedTime {
			return a.ID < z.ID
		}
		return a.CreatedTime < z.CreatedTime
	})
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

func (s *Store) Status(ctx context.Context) (Status, error) {
	status := Status{DBPath: s.path}
	counts := []struct {
		query string
		dest  *int
	}{
		{`select count(*) from spaces`, &status.Spaces},
		{`select count(*) from users`, &status.Users},
		{`select count(*) from teams`, &status.Teams},
		{`select count(*) from pages`, &status.Pages},
		{`select count(*) from blocks`, &status.Blocks},
		{`select count(*) from collections`, &status.Collections},
		{`select count(*) from comments`, &status.Comments},
		{`select count(*) from raw_records`, &status.RawRecords},
	}
	for _, count := range counts {
		if err := s.queryRowContext(ctx, count.query).Scan(count.dest); err != nil {
			return Status{}, err
		}
	}
	if err := s.queryRowContext(ctx, `select coalesce(max(synced_at), 0) from sync_state`).Scan(&status.LastSyncAt); err != nil {
		return Status{}, err
	}
	status.DBBytes = fileSize(s.path)
	status.WALBytes = fileSize(s.path + "-wal")
	return status, nil
}

func (s *Store) Optimize(ctx context.Context, vacuum bool) (MaintenanceSummary, error) {
	if err := s.RebuildFTS(ctx); err != nil {
		return MaintenanceSummary{}, err
	}
	for _, stmt := range []string{
		`insert into page_fts(page_fts) values('optimize')`,
		`insert into comment_fts(comment_fts) values('optimize')`,
		`pragma optimize`,
		`analyze`,
	} {
		if _, err := s.execContext(ctx, stmt); err != nil {
			return MaintenanceSummary{}, err
		}
	}
	if vacuum {
		if _, err := s.execContext(ctx, `vacuum`); err != nil {
			return MaintenanceSummary{}, err
		}
	}
	return MaintenanceSummary{
		RebuiltFTS: true,
		Optimized:  true,
		Analyzed:   true,
		Vacuumed:   vacuum,
		DBBytes:    fileSize(s.path),
		WALBytes:   fileSize(s.path + "-wal"),
		Message:    "database maintenance complete",
	}, nil
}

func fileSize(path string) int64 {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
