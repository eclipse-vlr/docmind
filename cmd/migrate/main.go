package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"docmind/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(); err != nil {
		// Connection/SQL errors can contain secrets; do not log raw driver errors.
		logger.Error("migration_failed")
		os.Exit(1)
	}
	logger.Info("migrations_applied")
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if strings.TrimSpace(dsn) == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrations.Apply(ctx, pool)
}
