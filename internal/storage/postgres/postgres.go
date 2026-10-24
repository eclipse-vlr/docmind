package postgres

import (
	"context"
	"errors"
	"time"

	"docmind/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string, timeout time.Duration) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Driver errors may contain credentials from malformed connection strings.
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 5
	cfg.ConnConfig.ConnectTimeout = timeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("initialize database pool")
	}
	s := &Store{pool: pool}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.Ready(checkCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Ready checks connectivity and the extension required by the application.
func (s *Store) Ready(ctx context.Context) error {
	var enabled bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')").Scan(&enabled)
	if err != nil {
		return errors.New("database readiness check failed")
	}
	if !enabled {
		return errors.New("database vector extension is not enabled")
	}
	return nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) SchemaReady(ctx context.Context) error {
	if err := s.Ready(ctx); err != nil {
		return err
	}
	var version string
	if err := s.pool.QueryRow(ctx, "SELECT version FROM schema_migrations WHERE version='004_embeddings.up.sql' AND EXISTS (SELECT 1 FROM schema_migrations WHERE version='002_file_deletion_retries.up.sql')").Scan(&version); err != nil {
		return errors.New("documents schema is not ready; run migrations")
	}
	return nil
}

// Migrate is explicitly invoked by integration tests; API startup never runs DDL.
func (s *Store) Migrate(ctx context.Context) error { return migrations.Apply(ctx, s.pool) }
