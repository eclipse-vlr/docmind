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
		{"", "", "", true}, {"fake", "", "", true}, {"yandex", "test-key", "", true},
		{"yandex", "", "", false}, {"yandex", "key\r\nsecret", "", false},
		{"other", "", "", false}, {"yandex", "key", "0s", false}, {"yandex", "key", "2m", false}, {"yandex", "key", "bad", false},
	} {
		env := map[string]string{"EMBEDDING_PROVIDER": tc.provider, "YANDEX_API_KEY": tc.key, "EMBEDDING_HTTP_TIMEOUT": tc.timeout, "YANDEX_FOLDER_ID": "test-folder"}
		p, err := Embeddings(func(k string) string { return env[k] }, llm.DocumentEmbedding)
		if (err == nil) != tc.valid {
			t.Fatalf("provider %q timeout %q: %v", tc.provider, tc.timeout, err)
		}
		if tc.valid && tc.provider != "yandex" && p.Model() != (llm.Fake{}).Model() {
			t.Fatal("default provider is not fake")
		}
	}
}

func TestYandexEmbeddingConfigRequiresFolderAndRejectsLegacyProvider(t *testing.T) {
	for _, purpose := range []llm.EmbeddingPurpose{llm.DocumentEmbedding, llm.QueryEmbedding} {
		env := map[string]string{"EMBEDDING_PROVIDER": "yandex", "YANDEX_API_KEY": "test-secret", "YANDEX_FOLDER_ID": "folder"}
		if _, err := Embeddings(func(k string) string { return env[k] }, purpose); err != nil {
			t.Fatal(err)
		}
		delete(env, "YANDEX_FOLDER_ID")
		if _, err := Embeddings(func(k string) string { return env[k] }, purpose); err == nil {
			t.Fatal("missing folder accepted")
		}
		env["EMBEDDING_PROVIDER"] = "openai"
		if _, err := Embeddings(func(k string) string { return env[k] }, purpose); err == nil {
			t.Fatal("removed provider accepted")
		}
	}
}
