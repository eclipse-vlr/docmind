package config

import (
	"errors"
	"time"

	"docmind/internal/llm"
)

// Embeddings chooses the document or query encoder within one shared space.
func Embeddings(getenv func(string) string, purpose llm.EmbeddingPurpose) (llm.EmbeddingProvider, error) {
	provider := getenv("EMBEDDING_PROVIDER")
	if provider == "" || provider == "fake" {
		return llm.Fake{}, nil
	}
	if provider != "yandex" {
		return nil, errors.New("EMBEDDING_PROVIDER must be fake or yandex")
	}
	timeout := 10 * time.Second
	if value := getenv("EMBEDDING_HTTP_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > time.Minute {
			return nil, errors.New("EMBEDDING_HTTP_TIMEOUT must be a positive duration up to 1m")
		}
	}
	return llm.NewYandexEmbeddings(getenv("YANDEX_API_KEY"), getenv("YANDEX_FOLDER_ID"), purpose, timeout)
}
