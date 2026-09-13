package store

import (
	"context"
	"database/sql"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"
)

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
	return s.WithSQLTransaction(ctx, func(*sql.Tx) error { return fn() })
}

func (s *Store) WithSQLTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.tx != nil {
		return fn(s.tx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	s.tx = tx
	err = fn(tx)
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
