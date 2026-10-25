package llm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

func newYandexClient(key, folder string, timeout time.Duration) (*http.Client, error) {
	if strings.TrimSpace(key) == "" || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return nil, errors.New("YANDEX_API_KEY is required and must be a valid API key")
	}
	if len(folder) == 0 || len(folder) > 64 {
		return nil, errors.New("YANDEX_FOLDER_ID is required and must be a folder ID")
	}
	for _, c := range folder {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return nil, errors.New("YANDEX_FOLDER_ID must be a folder ID")
		}
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("invalid provider HTTP timeout")
	}
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// yandexRequest makes one bounded call to a fixed provider endpoint. Retry
// ownership remains with the caller; errors never include keys or payloads.
func yandexRequest(ctx context.Context, client *http.Client, endpoint, key, folder string, body []byte, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Api-Key "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-folder-id", folder)
	res, err := client.Do(req)
	if err != nil {
		return nil, yandexTransportError(ctx, err)
	}
	defer res.Body.Close()
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
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, yandexTransportError(ctx, err)
	}
	if int64(len(raw)) > limit {
		return nil, ErrResponse
	}
	return raw, nil
}

func yandexTransportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrUnavailable
}
