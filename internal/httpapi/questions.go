package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"docmind/internal/documents"
	"docmind/internal/llm"
	"docmind/internal/rag"
)

const maxQuestionBody = 16 << 10

type QuestionService interface {
	Ask(context.Context, string, []string) (rag.Answer, error)
}

type QuestionOptions struct {
	Service QuestionService
	Timeout time.Duration
}

type questionHandlers struct {
	service QuestionService
	timeout time.Duration
	logger  *slog.Logger
	slots   chan struct{}
}

func registerQuestions(mux *http.ServeMux, options *QuestionOptions, logger *slog.Logger) {
	h := &questionHandlers{service: options.Service, timeout: options.Timeout, logger: logger, slots: make(chan struct{}, 2)}
	mux.HandleFunc("POST /api/v1/questions", h.ask)
}

func (h *questionHandlers) ask(w http.ResponseWriter, r *http.Request) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.fail(w, r, rag.ErrUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || r.Header.Get("Content-Encoding") != "" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Use application/json with UTF-8 encoding")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxQuestionBody)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Question body is too large")
			return
		}
		h.fail(w, r, rag.ErrInvalid)
		return
	}
	if !utf8.Valid(raw) {
		h.fail(w, r, rag.ErrInvalid)
		return
	}
	var body *struct {
		Question    string   `json:"question"`
		DocumentIDs []string `json:"document_ids"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body == nil || decoder.Decode(new(any)) != io.EOF {
		h.fail(w, r, rag.ErrInvalid)
		return
	}
	if err := rag.ValidateQuestion(body.Question, body.DocumentIDs); err != nil {
		h.fail(w, r, err)
		return
	}
	answer, err := h.service.Ask(ctx, body.Question, body.DocumentIDs)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func (h *questionHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "Question processing failed"
	switch {
	case errors.Is(err, rag.ErrInvalid):
		status, code, message = 400, "invalid_question", "Provide a nonempty question and 1-20 unique document UUIDs"
	case errors.Is(err, documents.ErrNotFound):
		status, code, message = 404, "document_not_found", "Document not found"
	case errors.Is(err, rag.ErrNotReady):
		status, code, message = 409, "documents_not_ready", "All selected documents must be ready"
	case errors.Is(err, rag.ErrModelMismatch):
		status, code, message = 409, "embedding_model_mismatch", "Reindex documents with the selected embedding provider"
	case errors.Is(err, llm.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		status, code, message = 504, "question_timeout", "Question processing timed out"
	case errors.Is(err, llm.ErrUnavailable):
		status, code, message = 503, "provider_unavailable", "Model provider is temporarily unavailable"
	case errors.Is(err, rag.ErrUnavailable):
		status, code, message = 503, "service_unavailable", "Question service is temporarily unavailable"
	case errors.Is(err, llm.ErrRejected), errors.Is(err, llm.ErrResponse), errors.Is(err, llm.ErrInput):
		status, code, message = 502, "provider_error", "Model provider could not produce a valid answer"
	case errors.Is(err, context.Canceled):
		status, code, message = 408, "request_canceled", "Request was canceled"
	}
	if status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	if status >= 500 {
		h.logger.WarnContext(r.Context(), "question_failed", "request_id", RequestID(r.Context()), "code", code)
	}
	writeError(w, status, code, message)
}
