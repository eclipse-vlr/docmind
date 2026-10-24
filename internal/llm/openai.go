package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const OpenAIModel = "text-embedding-3-small"
const maxResponseBytes = 2 << 20

type OpenAI struct {
	key      string
	client   *http.Client
	endpoint string
}

func NewOpenAI(key string, timeout time.Duration) (*OpenAI, error) {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("OPENAI_API_KEY is required for openai embeddings")
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("invalid embedding HTTP timeout")
	}
	return &OpenAI{key: key, endpoint: "https://api.openai.com/v1/embeddings", client: &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (*OpenAI) Model() string { return "openai:" + OpenAIModel }

func (p *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := validateInput(texts); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions"`
		Encoding   string   `json:"encoding_format"`
	}{OpenAIModel, texts, Dimensions, "float"})
	if err != nil {
		return nil, ErrInput
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	// Never include response bodies or transport errors in logs or returned errors.
	if res.StatusCode != http.StatusOK {
		switch {
		case res.StatusCode == 408 || res.StatusCode == 504:
			return nil, ErrTimeout
		case res.StatusCode == 429 || res.StatusCode >= 500:
			return nil, ErrUnavailable
		default:
			return nil, ErrRejected
		}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, ErrUnavailable
	}
	if len(raw) > maxResponseBytes {
		return nil, ErrResponse
	}
	var response struct {
		Model string `json:"model"`
		Data  []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Model != OpenAIModel || len(response.Data) != len(texts) {
		return nil, ErrResponse
	}
	out := make([][]float32, len(texts))
	for _, item := range response.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(out) || out[*item.Index] != nil || ValidateVector(item.Embedding) != nil {
			return nil, ErrResponse
		}
		out[*item.Index] = item.Embedding
	}
	return out, nil
}
