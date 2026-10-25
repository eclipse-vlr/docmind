package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"docmind/internal/documents"
)

type stubDocuments struct {
	uploads, calls int
	err            error
}

func (s *stubDocuments) Upload(_ context.Context, name string, body []byte) (documents.Document, error) {
	s.uploads++
	if s.err != nil {
		return documents.Document{}, s.err
	}
	_, _, err := documents.ValidateUpload(name, body)
	return documents.Document{ID: documents.NewID(), Status: "queued"}, err
}
func (s *stubDocuments) Get(_ context.Context, id string) (documents.Document, error) {
	s.calls++
	if !documents.ValidID(id) {
		return documents.Document{}, documents.ErrInvalid
	}
	return documents.Document{ID: id, StorageKey: "private"}, s.err
}
func (s *stubDocuments) List(context.Context, int, int) ([]documents.Document, error) {
	s.calls++
	return nil, s.err
}
func (s *stubDocuments) Delete(_ context.Context, id string) error {
	s.calls++
	if !documents.ValidID(id) {
		return documents.ErrInvalid
	}
	return s.err
}

func documentHandler(s DocumentService) http.Handler {
	return NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)), s, nil)
}

func uploadRequest(t *testing.T, name string, body []byte, extra bool) *http.Request {
	t.Helper()
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err := writer.WriteField("extra", "invalid"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/documents", &encoded)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}

func TestDocumentUpload(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		body           []byte
		extra          bool
		status         int
		calls          int
	}{
		{"valid", "a.txt", []byte("hello"), false, 202, 1},
		{"unsupported", "a.exe", []byte("hello"), false, 415, 1},
		{"empty", "a.txt", nil, false, 400, 1},
		{"extra part", "a.txt", []byte("hello"), true, 400, 0},
		{"oversized", "a.txt", bytes.Repeat([]byte("a"), documents.MaxFileSize+1), false, 413, 0},
		{"exact limit", "a.txt", bytes.Repeat([]byte("a"), documents.MaxFileSize), false, 202, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubDocuments{}
			w := httptest.NewRecorder()
			documentHandler(s).ServeHTTP(w, uploadRequest(t, tc.filename, tc.body, tc.extra))
			if w.Code != tc.status || s.uploads != tc.calls {
				t.Fatalf("status=%d uploads=%d body=%s", w.Code, s.uploads, w.Body.String())
			}
			if w.Code == 202 {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if !documents.ValidID(body["id"]) || body["status"] != "queued" || len(body) != 2 || w.Header().Get("Location") == "" {
					t.Fatal("invalid upload response", body)
				}
			}
		})
	}
}

func TestMalformedUploadHasNoEffects(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"json", "application/json", "{}", 415},
		{"no boundary", "multipart/form-data", "body", 400},
		{"malformed", "multipart/form-data; boundary=abc", "--abc\r\ninvalid", 400},
		{"no file", "multipart/form-data; boundary=abc", "--abc--\r\n", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubDocuments{}
			r := httptest.NewRequest("POST", "/api/v1/documents", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			documentHandler(s).ServeHTTP(w, r)
			if w.Code != tc.status || s.uploads != 0 {
				t.Fatalf("status=%d calls=%d", w.Code, s.uploads)
			}
		})
	}
	// No Content-Length: MaxBytesReader still bounds a streamed request.
	r := uploadRequest(t, "a.txt", bytes.Repeat([]byte("a"), documents.MaxFileSize+1), false)
	r.ContentLength = -1
	s := &stubDocuments{}
	w := httptest.NewRecorder()
	documentHandler(s).ServeHTTP(w, r)
	if w.Code != 413 || s.uploads != 0 {
		t.Fatal("streamed file limit not enforced")
	}
}

func TestDocumentRoutes(t *testing.T) {
	id := documents.NewID()
	for _, tc := range []struct {
		method, path string
		err          error
		status       int
	}{
		{"GET", "/api/v1/documents", nil, 200},
		{"GET", "/api/v1/documents?limit=0", nil, 400},
		{"GET", "/api/v1/documents?limit=101", nil, 400},
		{"GET", "/api/v1/documents?offset=-1", nil, 400},
		{"GET", "/api/v1/documents?offset=100001", nil, 400},
		{"GET", "/api/v1/documents?limit=1&limit=2", nil, 400},
		{"GET", "/api/v1/documents?limit=", nil, 400},
		{"GET", "/api/v1/documents?limit=%zz", nil, 400},
		{"GET", "/api/v1/documents/invalid", nil, 400},
		{"GET", "/api/v1/documents/" + id, nil, 200},
		{"GET", "/api/v1/documents/" + id, documents.ErrNotFound, 404},
		{"GET", "/api/v1/documents/" + id, errors.New("password=secret"), 503},
		{"DELETE", "/api/v1/documents/" + id, nil, 204},
		{"DELETE", "/api/v1/documents/" + id, documents.ErrNotFound, 404},
		{"PUT", "/api/v1/documents/" + id, nil, 405},
		{"PATCH", "/api/v1/documents", nil, 405},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			s := &stubDocuments{err: tc.err}
			w := httptest.NewRecorder()
			documentHandler(s).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private") {
				t.Fatal("internal data disclosed")
			}
			if tc.status == 204 && w.Body.Len() != 0 {
				t.Fatal("204 body is not empty")
			}
			if tc.path == "/api/v1/documents" && tc.status == 200 && !strings.Contains(w.Body.String(), `"items":[]`) {
				t.Fatal("empty list must be an array")
			}
		})
	}
}
