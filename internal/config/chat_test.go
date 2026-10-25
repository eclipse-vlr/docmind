package config

import (
	"strings"
	"testing"
	"time"

	"docmind/internal/llm"
)

func TestChatConfig(t *testing.T) {
	provider, err := Chat(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(llm.FakeChat); !ok {
		t.Fatal("default chat must run offline")
	}
	for _, tc := range []struct {
		name  string
		env   map[string]string
		valid bool
	}{
		{"fake", map[string]string{"CHAT_PROVIDER": "fake"}, true},
		{"unknown provider", map[string]string{"CHAT_PROVIDER": "unknown"}, false},
		{"removed provider", map[string]string{"CHAT_PROVIDER": "openai"}, false},
		{"missing folder", map[string]string{"CHAT_PROVIDER": "yandex", "YANDEX_API_KEY": "test-secret"}, false},
		{"missing key", map[string]string{"CHAT_PROVIDER": "yandex"}, false},
		{"yandex", map[string]string{"CHAT_PROVIDER": "yandex", "YANDEX_API_KEY": "test-secret", "YANDEX_FOLDER_ID": "test-folder"}, true},
		{"bad timeout", map[string]string{"CHAT_PROVIDER": "yandex", "YANDEX_API_KEY": "test-secret", "YANDEX_FOLDER_ID": "test-folder", "CHAT_HTTP_TIMEOUT": "bad"}, false},
		{"zero timeout", map[string]string{"CHAT_PROVIDER": "yandex", "YANDEX_API_KEY": "test-secret", "YANDEX_FOLDER_ID": "test-folder", "CHAT_HTTP_TIMEOUT": "0s"}, false},
		{"large timeout", map[string]string{"CHAT_PROVIDER": "yandex", "YANDEX_API_KEY": "test-secret", "YANDEX_FOLDER_ID": "test-folder", "CHAT_HTTP_TIMEOUT": "2m"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Chat(func(k string) string { return tc.env[k] })
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "test-secret") {
				t.Fatal("key leaked")
			}
		})
	}
}

func TestQuestionTimeout(t *testing.T) {
	for _, tc := range []struct {
		value string
		write time.Duration
		want  time.Duration
	}{
		{"", 30 * time.Second, 20 * time.Second}, {"5s", 30 * time.Second, 5 * time.Second},
		{"bad", 30 * time.Second, 0}, {"0s", 30 * time.Second, 0}, {"-1s", 30 * time.Second, 0},
		{"30s", 30 * time.Second, 0}, {"", 10 * time.Second, 0},
	} {
		t.Run(tc.value+tc.write.String(), func(t *testing.T) {
			d, err := QuestionTimeout(func(string) string { return tc.value }, tc.write)
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid timeout accepted")
				}
			} else if err != nil || d != tc.want {
				t.Fatalf("duration=%s error=%v", d, err)
			}
		})
	}
}
