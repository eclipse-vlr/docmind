package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"docmind/internal/config"
	"docmind/internal/ingestion"
	"docmind/internal/llm"
	"docmind/internal/storage/local"
	"docmind/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("worker_failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, settings, err := config.LoadWorker(os.Getenv)
	if err != nil {
		return fmt.Errorf("load worker configuration: %w", err)
	}
	embeddings, err := config.Embeddings(os.Getenv, llm.DocumentEmbedding)
	if err != nil {
		return err
	}
	parser := ingestion.TextParser{}
	if err := parser.Check(); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DBTimeout)
	if err != nil {
		return err
	}
	defer db.Close()
	checkCtx, cancel := context.WithTimeout(ctx, cfg.DBTimeout)
	err = db.SchemaReady(checkCtx)
	cancel()
	if err != nil {
		return err
	}
	files, err := local.Open(cfg.StorageDir)
	if err != nil {
		return errors.New("open private file storage failed")
	}
	defer files.Close()
	w, err := ingestion.NewWorker(db, files, parser, embeddings, settings, logger)
	if err != nil {
		return err
	}
	logger.Info("worker_started", "max_attempts", settings.MaxAttempts, "embedding_model", embeddings.Model())
	err = w.Run(ctx)
	if errors.Is(err, context.Canceled) {
		logger.Info("worker_stopped")
		return nil
	}
	return err
}
