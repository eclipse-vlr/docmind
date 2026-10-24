package config

import (
	"testing"
	"time"
)

func TestWorkerConfig(t *testing.T) {
	get := func(string) string { return "" }
	if _, _, err := LoadWorker(get); err == nil {
		t.Fatal("missing DB accepted")
	}
	for _, tc := range []struct{ key, value string }{
		{"WORKER_JOB_TIMEOUT", "0s"}, {"WORKER_LEASE_DURATION", "30s"},
		{"WORKER_POLL_INTERVAL", "invalid"}, {"WORKER_MAX_ATTEMPTS", "11"},
		{"WORKER_MAX_ATTEMPTS", "-1"}, {"WORKER_JOB_TIMEOUT", "2h"},
		{"WORKER_LEASE_DURATION", "2562047h"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, _, err := LoadWorker(func(k string) string {
				if k == "DATABASE_URL" {
					return "postgres://localhost/docmind"
				}
				if k == tc.key {
					return tc.value
				}
				return ""
			})
			if err == nil {
				t.Fatal("invalid settings accepted")
			}
		})
	}
	_, s, err := LoadWorker(func(k string) string {
		if k == "DATABASE_URL" {
			return "postgres://localhost/docmind"
		}
		return ""
	})
	if err != nil || s.MaxAttempts != 3 || s.LeaseDuration != time.Minute || s.JobTimeout != 30*time.Second {
		t.Fatalf("settings=%+v error=%v", s, err)
	}
}
