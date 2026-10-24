package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"docmind/internal/config"
	"docmind/internal/rag"
	"docmind/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	question := flag.String("question", "", "search query")
	ids := flag.String("documents", "", "comma-separated document UUIDs")
	k := flag.Int("top-k", 5, "number of fragments (1-20)")
	flag.Parse()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	provider, err := config.Embeddings(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DBTimeout)
	if err != nil {
		return errors.New("search database unavailable")
	}
	defer db.Close()
	if err := db.SchemaReady(ctx); err != nil {
		return errors.New("search schema not ready; run migrations")
	}
	matches, err := rag.NewSearcher(db, provider).Search(ctx, *question, strings.Split(*ids, ","), *k)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Matches []rag.Match `json:"matches"`
	}{matches})
}
