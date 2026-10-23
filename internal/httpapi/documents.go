package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"docmind/internal/documents"
)

const maxUploadBody = documents.MaxFileSize + (64 << 10)

type DocumentService interface {
	Upload(context.Context, string, []byte) (documents.Document, error)
	Get(context.Context, string) (documents.Document, error)
	List(context.Context, int, int) ([]documents.Document, error)
	Delete(context.Context, string) error
}

type documentHandlers struct {
	service DocumentService
	timeout time.Duration
	logger  *slog.Logger
	uploads chan struct{}
}

func registerDocuments(mux *http.ServeMux, service DocumentService, timeout time.Duration, logger *slog.Logger) {
	h := &documentHandlers{service: service, timeout: timeout, logger: logger, uploads: make(chan struct{}, 4)}
	mux.HandleFunc("POST /api/v1/documents", h.upload)
	mux.HandleFunc("GET /api/v1/documents", h.list)
	mux.HandleFunc("GET /api/v1/documents/{id}", h.get)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", h.delete)
}

func (h *documentHandlers) upload(w http.ResponseWriter, r *http.Request) {
	select {
	case h.uploads <- struct{}{}:
		defer func() { <-h.uploads }()
	default:
		h.fail(w, r, documents.ErrUnavailable)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || r.Header.Get("Content-Encoding") != "" {
		h.fail(w, r, documents.ErrUnsupported)
		return
	}
	if r.ContentLength > maxUploadBody {
		h.fail(w, r, documents.ErrTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	reader, err := r.MultipartReader()
	if err != nil {
		h.fail(w, r, documents.ErrInvalid)
		return
	}
	part, err := reader.NextRawPart()
	if err != nil {
		h.uploadError(w, r, err)
		return
	}
	if part.FormName() != "file" || part.FileName() == "" || part.Header.Get("Content-Transfer-Encoding") != "" {
		h.fail(w, r, documents.ErrInvalid)
		return
	}
	name := part.FileName()
	body, err := io.ReadAll(io.LimitReader(part, documents.MaxFileSize+1))
	if err != nil {
		h.uploadError(w, r, err)
		return
	}
	if len(body) > documents.MaxFileSize {
		h.fail(w, r, documents.ErrTooLarge)
		return
	}
	// Validate the entire multipart envelope before creating any durable state.
	if _, err := reader.NextRawPart(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = documents.ErrInvalid
		}
		h.uploadError(w, r, err)
		return
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		h.uploadError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	d, err := h.service.Upload(ctx, name, body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/documents/"+d.ID)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": d.ID, "status": d.Status})
}

func (h *documentHandlers) uploadError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		h.fail(w, r, documents.ErrTooLarge)
		return
	}
	h.fail(w, r, documents.ErrInvalid)
}

func (h *documentHandlers) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	d, err := h.service.Get(ctx, strings.ToLower(r.PathValue("id")))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *documentHandlers) list(w http.ResponseWriter, r *http.Request) {
	values, err := parsePagination(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	items, err := h.service.List(ctx, values[0], values[1])
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []documents.Document{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": values[0], "offset": values[1]})
}

func parsePagination(r *http.Request) ([2]int, error) {
	result := [2]int{20, 0}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return result, documents.ErrInvalid
	}
	for i, key := range []string{"limit", "offset"} {
		if values, ok := query[key]; ok {
			if len(values) != 1 {
				return result, documents.ErrInvalid
			}
			result[i], err = strconv.Atoi(values[0])
			if err != nil {
				return result, documents.ErrInvalid
			}
		}
	}
	if result[0] < 1 || result[0] > 100 || result[1] < 0 || result[1] > 100000 {
		return result, documents.ErrInvalid
	}
	return result, nil
}

func (h *documentHandlers) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	if err := h.service.Delete(ctx, strings.ToLower(r.PathValue("id"))); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *documentHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, documents.ErrInvalid):
		writeError(w, 400, "invalid_request", "Invalid document request")
	case errors.Is(err, documents.ErrTooLarge):
		writeError(w, 413, "file_too_large", "File must not exceed 10 MiB")
	case errors.Is(err, documents.ErrUnsupported):
		writeError(w, 415, "unsupported_format", "Expected UTF-8 TXT, Markdown or PDF")
	case errors.Is(err, documents.ErrNotFound):
		writeError(w, 404, "not_found", "Document not found")
	default:
		h.logger.ErrorContext(r.Context(), "document_operation_failed", "request_id", RequestID(r.Context()), "outcome_unknown", errors.Is(err, documents.ErrCommitUnknown))
		writeError(w, 503, "unavailable", "Document service is unavailable")
	}
}
