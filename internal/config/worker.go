package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"docmind/internal/ingestion"
)

func LoadWorker(getenv func(string) string) (Config, ingestion.Settings, error) {
	cfg, err := Load(getenv)
	if err != nil {
		return Config{}, ingestion.Settings{}, err
	}
	s := ingestion.Settings{MaxAttempts: 3, DBTimeout: cfg.DBTimeout}
	for _, setting := range []struct {
		name     string
		fallback time.Duration
		target   *time.Duration
	}{
		{"WORKER_POLL_INTERVAL", time.Second, &s.PollInterval},
		{"WORKER_JOB_TIMEOUT", 30 * time.Second, &s.JobTimeout},
		{"WORKER_LEASE_DURATION", time.Minute, &s.LeaseDuration},
	} {
		*setting.target = setting.fallback
		if value := getenv(setting.name); value != "" {
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 || d > time.Hour {
				return Config{}, s, fmt.Errorf("%s must be a positive duration up to 1h", setting.name)
			}
			*setting.target = d
		}
	}
	if value := getenv("WORKER_MAX_ATTEMPTS"); value != "" {
		s.MaxAttempts, err = strconv.Atoi(value)
		if err != nil || s.MaxAttempts < 1 || s.MaxAttempts > 10 {
			return Config{}, s, errors.New("WORKER_MAX_ATTEMPTS must be between 1 and 10")
		}
	}
	if cfg.DBTimeout > time.Hour || s.LeaseDuration <= s.JobTimeout+2*s.DBTimeout {
		return Config{}, s, errors.New("WORKER_LEASE_DURATION must exceed WORKER_JOB_TIMEOUT + 2 * DB_TIMEOUT")
	}
	return cfg, s, nil
}
