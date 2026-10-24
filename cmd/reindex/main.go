package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"docmind/internal/config"
	"docmind/internal/documents"
	"docmind/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	id := flag.String("document", "", "document UUID to reindex")
	flag.Parse()
	if !documents.ValidID(*id) {
		return errors.New("a valid -document UUID is required")
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DBTimeout)
	defer cancel()
	db, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DBTimeout)
	if err != nil {
		return errors.New("reindex database unavailable")
	}
	defer db.Close()
	if err := db.SchemaReady(ctx); err != nil {
		return errors.New("reindex schema not ready; run migrations")
	}
	if err := db.RequeueDocument(ctx, *id); err != nil {
		if errors.Is(err, documents.ErrNotFound) {
			return documents.ErrNotFound
		}
		return errors.New("reindex failed")
	}
	fmt.Fprintln(os.Stdout, "Document queued for indexing")
	return nil
}
