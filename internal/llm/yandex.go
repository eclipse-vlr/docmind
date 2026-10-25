package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Both encoders produce vectors in the same space; their input roles differ.
type EmbeddingPurpose string

const (
	DocumentEmbedding         EmbeddingPurpose = "doc"
	QueryEmbedding            EmbeddingPurpose = "query"
	yandexEmbeddingSpace                       = "yandex:text-embeddings-v2:256"
	maxEmbeddingResponseBytes                  = 64 << 10
)

type YandexEmbeddings struct {
	key      string
	folder   string
	modelURI string
	client   *http.Client
	endpoint string
}

func NewYandexEmbeddings(key, folder string, purpose EmbeddingPurpose, timeout time.Duration) (*YandexEmbeddings, error) {
	if purpose != DocumentEmbedding && purpose != QueryEmbedding {
		return nil, errors.New("invalid embedding purpose")
	}
	client, err := newYandexClient(key, folder, timeout)
	if err != nil {
		return nil, err
	}
	return &YandexEmbeddings{key: key, folder: folder, modelURI: "emb://" + folder + "/text-embeddings-v2-" + string(purpose) + "/", client: client, endpoint: "https://ai.api.cloud.yandex.net/v1/embeddings"}, nil
}

func (*YandexEmbeddings) Model() string { return yandexEmbeddingSpace }

func (p *YandexEmbeddings) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := validateInput(texts); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		// The compatible API accepts exactly one input per HTTP request. Keep
		// worker batches bounded and preserve order without spawning goroutines.
		body, err := json.Marshal(struct {
			Model      string `json:"model"`
			Input      string `json:"input"`
			Dimensions int    `json:"dimensions"`
			Encoding   string `json:"encoding_format"`
		}{p.modelURI, text, Dimensions, "float"})
		if err != nil {
			return nil, ErrInput
		}
		raw, err := yandexRequest(ctx, p.client, p.endpoint, p.key, p.folder, body, maxEmbeddingResponseBytes)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(raw) {
			return nil, ErrResponse
		}
		var response struct {
			Model string `json:"model"`
			Data  []struct {
				Index     *int       `json:"index"`
				Embedding []*float32 `json:"embedding"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &response) != nil || len(response.Data) != 1 || normalizeEmbeddingURI(response.Model) != normalizeEmbeddingURI(p.modelURI) {
			return nil, ErrResponse
		}
		item := response.Data[0]
		if item.Index == nil || *item.Index != 0 || len(item.Embedding) != Dimensions {
			return nil, ErrResponse
		}
		vector := make([]float32, Dimensions)
		for j, value := range item.Embedding {
			if value == nil {
				return nil, ErrResponse
			}
			vector[j] = *value
		}
		if err := ValidateVector(vector); err != nil {
			return nil, err
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func normalizeEmbeddingURI(uri string) string {
	return strings.TrimSuffix(strings.TrimRight(uri, "/"), "/latest")
}
