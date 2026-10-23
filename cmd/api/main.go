package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docmind/internal/config"
	"docmind/internal/documents"
	"docmind/internal/httpapi"
	"docmind/internal/storage/local"
	"docmind/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("application_failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DBTimeout)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	checkCtx, cancelCheck := context.WithTimeout(ctx, cfg.DBTimeout)
	err = db.SchemaReady(checkCtx)
	cancelCheck()
	if err != nil {
		return err
	}
	files, err := local.Open(cfg.StorageDir)
	if err != nil {
		return errors.New("open private file storage failed")
	}
	defer files.Close()
	service := documents.NewService(db, files)
	cleanupCtx, cancelCleanup := context.WithCancel(context.Background())
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			opCtx, cancel := context.WithTimeout(cleanupCtx, cfg.DBTimeout)
			if err := service.Cleanup(opCtx); err != nil && cleanupCtx.Err() == nil {
				logger.Warn("file_cleanup_failed")
			}
			cancel()
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	// Stop cleanup before closing its file and database dependencies.
	defer func() { cancelCleanup(); <-cleanupDone }()
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandler(db.SchemaReady, cfg.DBTimeout, logger, service),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	logger.Info("http_started", "address", listener.Addr().String())
	select {
	case err := <-result:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		// Restore default signal behavior so a second signal can force exit.
		stop()
	}
	logger.Info("shutdown_started")
	// Do not inherit the canceled signal context: accepted requests may finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		<-result
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	logger.Info("shutdown_completed")
	return nil
}
