package config

import (
	"testing"

	"docmind/internal/llm"
)

func TestEmbeddingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		provider, key, timeout string
		valid                  bool
	}{
		{"", "", "", true}, {"fake", "", "", true}, {"openai", "test-key", "", true},
		{"openai", "", "", false}, {"openai", "key\r\nsecret", "", false},
		{"other", "", "", false}, {"openai", "key", "0s", false}, {"openai", "key", "2m", false}, {"openai", "key", "bad", false},
	} {
		env := map[string]string{"EMBEDDING_PROVIDER": tc.provider, "OPENAI_API_KEY": tc.key, "EMBEDDING_HTTP_TIMEOUT": tc.timeout}
		p, err := Embeddings(func(k string) string { return env[k] })
		if (err == nil) != tc.valid {
			t.Fatalf("provider %q timeout %q: %v", tc.provider, tc.timeout, err)
		}
		if tc.valid && tc.provider != "openai" && p.Model() != (llm.Fake{}).Model() {
			t.Fatal("default provider is not fake")
		}
	}
}
