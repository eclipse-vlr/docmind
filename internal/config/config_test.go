package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://user:password@localhost/docmind"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" || cfg.LogLevel != slog.LevelInfo ||
		cfg.ReadHeaderTimeout != 5*time.Second || cfg.ReadTimeout != 15*time.Second ||
		cfg.WriteTimeout != 30*time.Second || cfg.IdleTimeout != time.Minute ||
		cfg.ShutdownTimeout != 10*time.Second || cfg.DBTimeout != 3*time.Second || cfg.StorageDir != "data/documents" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://user:password@localhost/docmind", "HTTP_ADDR": ":8090", "LOG_LEVEL": "DEBUG",
		"HTTP_READ_HEADER_TIMEOUT": "1s", "HTTP_READ_TIMEOUT": "2s", "HTTP_WRITE_TIMEOUT": "4s",
		"HTTP_IDLE_TIMEOUT": "5s", "SHUTDOWN_TIMEOUT": "6s", "DB_TIMEOUT": "500ms",
		"STORAGE_DIR": "private/documents",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != env["DATABASE_URL"] || cfg.HTTPAddr != ":8090" || cfg.LogLevel != slog.LevelDebug ||
		cfg.ReadHeaderTimeout != time.Second || cfg.ReadTimeout != 2*time.Second || cfg.WriteTimeout != 4*time.Second ||
		cfg.IdleTimeout != 5*time.Second || cfg.ShutdownTimeout != 6*time.Second || cfg.DBTimeout != 500*time.Millisecond || cfg.StorageDir != "private/documents" {
		t.Fatalf("overrides were not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"missing database", "DATABASE_URL", ""}, {"blank database", "DATABASE_URL", " "},
		{"missing port", "HTTP_ADDR", "localhost"}, {"bad port", "HTTP_ADDR", ":abc"},
		{"zero port", "HTTP_ADDR", ":0"}, {"large port", "HTTP_ADDR", ":65536"},
		{"log level", "LOG_LEVEL", "verbose"},
		{"blank storage", "STORAGE_DIR", " "},
		{"read header exceeds read", "HTTP_READ_HEADER_TIMEOUT", "20s"},
		{"database exceeds write", "DB_TIMEOUT", "30s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://user:secret@localhost/docmind", tc.key: tc.value}
			_, err := Load(func(key string) string { return env[key] })
			if err == nil {
				t.Fatal("expected validation error")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error disclosed credentials")
			}
		})
	}
	for _, key := range []string{"HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT", "DB_TIMEOUT"} {
		for _, value := range []string{"0s", "-1s", "invalid", "999999999999999h"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				_, err := Load(func(name string) string {
					if name == key {
						return value
					}
					if name == "DATABASE_URL" {
						return "postgres://localhost/docmind"
					}
					return ""
				})
				if err == nil {
					t.Fatal("expected validation error")
				}
			})
		}
	}
}
