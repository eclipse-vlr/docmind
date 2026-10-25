package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"docmind/internal/documents"
	"docmind/internal/httpapi"
	"docmind/internal/ingestion"
	"docmind/internal/llm"
	"docmind/internal/rag"
)

func questionsHandler(f *fixture, chat llm.ChatProvider) http.Handler {
	s := rag.NewQuestions(rag.NewSearcher(f.repo, llm.Fake{}), chat)
	return httpapi.NewHandler(f.repo.SchemaReady, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)), f.service, &httpapi.QuestionOptions{Service: s, Timeout: 2 * time.Second})
}

func askHTTP(t *testing.T, h http.Handler, question string, ids []string, status int) rag.Answer {
	t.Helper()
	body, err := json.Marshal(map[string]any{"question": question, "document_ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/questions", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("question=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
	if status != 200 {
		var payload struct {
			Error struct{ Code, Message string }
		}
		if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.Error.Code == "" {
			t.Fatal("invalid error envelope")
		}
		return rag.Answer{}
	}
	var out rag.Answer
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func verifySources(t *testing.T, f *fixture, answer rag.Answer, allowed map[string]bool) {
	t.Helper()
	if len(answer.Sources) == 0 {
		t.Fatal("answer has no sources")
	}
	for _, s := range answer.Sources {
		var docID, text string
		var page *int
		if err := f.pool.QueryRow(context.Background(), "SELECT document_id::text,content,page_number FROM document_chunks WHERE id=$1", s.ChunkID).Scan(&docID, &text, &page); err != nil {
			t.Fatal("invented chunk", err)
		}
		if !allowed[docID] || s.DocumentID != docID || s.Snippet != text || (s.Page == nil) != (page == nil) || (page != nil && *s.Page != *page) {
			t.Fatal("source differs from indexed chunk", s)
		}
	}
}

func TestQuestionsUploadToAnswerWithVerifiedSources(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	h := questionsHandler(f, llm.FakeChat{})
	d := upload(t, h, "first.txt")
	askHTTP(t, h, "Document", []string{d.ID}, 409)
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	out := askHTTP(t, h, "Document", []string{d.ID}, 200)
	if out.Answer != "Document contents" {
		t.Fatal("unexpected fake answer", out)
	}
	verifySources(t, f, out, map[string]bool{d.ID: true})
	oldChunk := out.Sources[0].ChunkID
	if err := f.repo.RequeueDocument(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	askHTTP(t, h, "Document", []string{d.ID}, 409)
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	out = askHTTP(t, h, "Document", []string{d.ID}, 200)
	verifySources(t, f, out, map[string]bool{d.ID: true})
	if out.Sources[0].ChunkID == oldChunk {
		t.Fatal("reindex published obsolete source")
	}
	if err := f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	askHTTP(t, h, "Document", []string{d.ID}, 404)
}

func TestQuestionsRespectDocumentSelectionAndMissingContext(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	h := questionsHandler(f, llm.FakeChat{})
	a := queuedDocument(t, f, "PostgreSQL stores indexed documents.")
	b, err := f.service.Upload(ctx, "animals.md", []byte("Cats have whiskers."))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	out := askHTTP(t, h, "PostgreSQL", []string{b.ID}, 200)
	if out.Answer != rag.InsufficientAnswer || out.Sources == nil || len(out.Sources) != 0 {
		t.Fatal("excluded content leaked", out)
	}
	out = askHTTP(t, h, "PostgreSQL", []string{a.ID, b.ID}, 200)
	verifySources(t, f, out, map[string]bool{a.ID: true})
	askHTTP(t, h, "PostgreSQL", []string{a.ID, documents.NewID()}, 404)
	if _, err := f.pool.Exec(ctx, "UPDATE documents SET embedding_model='different-model' WHERE id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	askHTTP(t, h, "PostgreSQL", []string{a.ID}, 409)
}

func TestQuestionsPDFSourcesKeepRealPage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.service.Upload(ctx, "pages.pdf", questionPDF())
	if err != nil {
		t.Fatal(err)
	}
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	out := askHTTP(t, questionsHandler(f, llm.FakeChat{}), "PostgreSQL", []string{d.ID}, 200)
	verifySources(t, f, out, map[string]bool{d.ID: true})
	if out.Sources[0].Page == nil || *out.Sources[0].Page != 2 {
		t.Fatal("PDF page lost", out)
	}
}

type questionChatFunc func(context.Context, llm.ChatRequest) (llm.ChatResult, error)

func (f questionChatFunc) Chat(ctx context.Context, in llm.ChatRequest) (llm.ChatResult, error) {
	return f(ctx, in)
}

func TestQuestionsProviderFailuresAndFabricatedSources(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "PostgreSQL stores documents.")
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{llm.ErrUnavailable, 503}, {llm.ErrTimeout, 504}, {llm.ErrRejected, 502}, {llm.ErrResponse, 502}, {errors.New("sensitive private diagnostics"), 500}} {
		chat := questionChatFunc(func(context.Context, llm.ChatRequest) (llm.ChatResult, error) { return llm.ChatResult{}, tc.err })
		askHTTP(t, questionsHandler(f, chat), "PostgreSQL", []string{d.ID}, tc.status)
	}
	chat := questionChatFunc(func(context.Context, llm.ChatRequest) (llm.ChatResult, error) {
		return llm.ChatResult{Answer: "fabricated answer", SourceIDs: []string{"s99"}}, nil
	})
	askHTTP(t, questionsHandler(f, chat), "PostgreSQL", []string{d.ID}, 502)
}

func questionPDF() []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [4 0 R 6 0 R] /Count 2 >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	for _, text := range []string{"Cats have whiskers.", "PostgreSQL stores documents."} {
		n := len(objects) + 1
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", n+1), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	var out strings.Builder
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(out.String())
}
