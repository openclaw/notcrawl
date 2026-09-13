package store

import (
	"context"
	"os"
	"strings"
)

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
