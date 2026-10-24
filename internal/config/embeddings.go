package config

import (
	"errors"
	"time"

	"docmind/internal/llm"
)

// Embeddings constructs the same fixed vector space for worker and search.
func Embeddings(getenv func(string) string) (llm.EmbeddingProvider, error) {
	provider := getenv("EMBEDDING_PROVIDER")
	if provider == "" || provider == "fake" {
		return llm.Fake{}, nil
	}
	if provider != "openai" {
		return nil, errors.New("EMBEDDING_PROVIDER must be fake or openai")
	}
	timeout := 10 * time.Second
	if value := getenv("EMBEDDING_HTTP_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > time.Minute {
			return nil, errors.New("EMBEDDING_HTTP_TIMEOUT must be a positive duration up to 1m")
		}
	}
	return llm.NewOpenAI(getenv("OPENAI_API_KEY"), timeout)
}
