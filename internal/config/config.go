package config

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	LogLevel          slog.Level
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	DBTimeout         time.Duration
	StorageDir        string
}

// Load reads environment settings through getenv so tests do not mutate process state.
func Load(getenv func(string) string) (Config, error) {
	c := Config{HTTPAddr: "127.0.0.1:8080", DatabaseURL: getenv("DATABASE_URL"), StorageDir: "data/documents"}
	if value := getenv("STORAGE_DIR"); value != "" {
		if strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("STORAGE_DIR must not be blank")
		}
		c.StorageDir = value
	}
	if value := getenv("HTTP_ADDR"); value != "" {
		c.HTTPAddr = value
	}
	_, port, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if value := getenv("LOG_LEVEL"); value != "" {
		if err := c.LogLevel.UnmarshalText([]byte(value)); err != nil {
			return Config{}, fmt.Errorf("LOG_LEVEL must be DEBUG, INFO, WARN or ERROR")
		}
	}
	for _, setting := range []struct {
		name     string
		fallback time.Duration
		target   *time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", 5 * time.Second, &c.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", 15 * time.Second, &c.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", 30 * time.Second, &c.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", 60 * time.Second, &c.IdleTimeout},
		{"SHUTDOWN_TIMEOUT", 10 * time.Second, &c.ShutdownTimeout},
		{"DB_TIMEOUT", 3 * time.Second, &c.DBTimeout},
	} {
		value := getenv(setting.name)
		*setting.target = setting.fallback
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", setting.name)
		}
		*setting.target = duration
	}
	if c.ReadHeaderTimeout > c.ReadTimeout {
		return Config{}, fmt.Errorf("HTTP_READ_HEADER_TIMEOUT must not exceed HTTP_READ_TIMEOUT")
	}
	if c.DBTimeout >= c.WriteTimeout {
		return Config{}, fmt.Errorf("DB_TIMEOUT must be less than HTTP_WRITE_TIMEOUT")
	}
	return c, nil
}
