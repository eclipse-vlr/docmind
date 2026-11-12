package smoke

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRunningService exercises real HTTP, worker, storage and database processes.
// Run only against an isolated instance configured with both fake providers.
func TestRunningService(t *testing.T) {
	base := strings.TrimRight(os.Getenv("DOCMIND_BASE_URL"), "/")
	if base == "" {
		t.Skip("DOCMIND_BASE_URL is not set")
	}
	client := &http.Client{Timeout: 35 * time.Second}
	call := func(method, path, content string, body io.Reader, status int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if content != "" {
			req.Header.Set("Content-Type", content)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, data)
		}
		return data
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		res, err := client.Get(base + "/readyz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("service never became ready")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if body := call("GET", "/", "", nil, 200); !bytes.Contains(body, []byte("DocMind")) {
		t.Fatal("workspace not served")
	}
	call("GET", "/openapi.yaml", "", nil, 200)
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "smoke.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(part, "DocMind stores documents in PostgreSQL."); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	var document struct{ ID, Status string }
	if err = json.Unmarshal(call("POST", "/api/v1/documents", writer.FormDataContentType(), &form, 202), &document); err != nil || document.ID == "" {
		t.Fatal("invalid upload response", err)
	}
	defer func() {
		req, _ := http.NewRequest("DELETE", base+"/api/v1/documents/"+document.ID, nil)
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
		}
	}()
	deadline = time.Now().Add(30 * time.Second)
	for {
		var d struct{ Status string }
		if err = json.Unmarshal(call("GET", "/api/v1/documents/"+document.ID, "", nil, 200), &d); err != nil {
			t.Fatal(err)
		}
		if d.Status == "ready" {
			break
		}
		if d.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("indexing stopped at %s", d.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	question, _ := json.Marshal(map[string]any{"question": "Where does DocMind store documents?", "document_ids": []string{document.ID}})
	var answer struct {
		Answer  string
		Sources []struct {
			DocumentID string `json:"document_id"`
			ChunkID    string `json:"chunk_id"`
			Snippet    string
		}
	}
	if err = json.Unmarshal(call("POST", "/api/v1/questions", "application/json", bytes.NewReader(question), 200), &answer); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Answer, "PostgreSQL") || len(answer.Sources) == 0 {
		t.Fatal("missing grounded answer")
	}
	for _, s := range answer.Sources {
		if s.DocumentID != document.ID || s.ChunkID == "" || !strings.Contains(s.Snippet, "PostgreSQL") {
			t.Fatal("invalid source")
		}
	}
	metrics := call("GET", "/metrics", "", nil, 200)
	if !bytes.Contains(metrics, []byte("docmind_http_requests_total")) || !bytes.Contains(metrics, []byte("docmind_ai_embedding_checkpoints")) || bytes.Contains(metrics, []byte(document.ID)) {
		t.Fatal("invalid metrics")
	}
	call("DELETE", "/api/v1/documents/"+document.ID, "", nil, 204)
	call("GET", "/api/v1/documents/"+document.ID, "", nil, 404)
}
