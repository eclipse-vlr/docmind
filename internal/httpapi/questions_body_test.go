package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"docmind/internal/documents"
	"docmind/internal/rag"
)

// Regression tests for request-body cancellation and admission after expiry.
func TestQuestionSlowHTTPBodyHonorsDeadline(t *testing.T) {
	qTimeout := 20 * time.Millisecond
	s := questionFunc(func(ctx context.Context, _ string, _ []string) (rag.Answer, error) {
		return rag.Answer{Answer: "answer", Sources: []rag.Source{}}, ctx.Err()
	})
	h := questionHandler(s, qTimeout, io.Discard)
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ReadTimeout = 250 * time.Millisecond
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// Claim a finite body but send only its prefix and keep the connection open.
	_, err = fmt.Fprint(conn, "POST /api/v1/questions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{\"question\":")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("question timeout=%s, HTTP read timeout=250ms, elapsed=%s, status=%d, body=%s", qTimeout, elapsed, response.StatusCode, body)
	if response.StatusCode != http.StatusGatewayTimeout || elapsed > 150*time.Millisecond {
		t.Errorf("deadline not enforced while reading body: got status=%d after %s, want 504 within question budget", response.StatusCode, elapsed)
	}
}

type questionGatedBody struct {
	reader      *strings.Reader
	started     chan<- struct{}
	release     chan struct{}
	onceStarted sync.Once
	onceClose   sync.Once
}

func (b *questionGatedBody) Read(p []byte) (int, error) {
	b.onceStarted.Do(func() { b.started <- struct{}{} })
	<-b.release
	return b.reader.Read(p)
}
func (b *questionGatedBody) Close() error { b.onceClose.Do(func() { close(b.release) }); return nil }

func TestQuestionExpiredBodyReadsReleaseAdmission(t *testing.T) {
	s := questionFunc(func(ctx context.Context, _ string, _ []string) (rag.Answer, error) {
		return rag.Answer{Answer: "answer", Sources: []rag.Source{}}, ctx.Err()
	})
	h := NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)), nil, &QuestionOptions{Service: s, Timeout: 20 * time.Millisecond})
	started := make(chan struct{}, 2)
	var bodies []*questionGatedBody
	var wg sync.WaitGroup
	defer func() {
		for _, b := range bodies {
			_ = b.Close()
		}
		wg.Wait()
	}()
	for range 2 {
		body := &questionGatedBody{reader: strings.NewReader(questionBody()), started: started, release: make(chan struct{})}
		bodies = append(bodies, body)
		r := questionRequest(questionBody())
		r.Body = body
		r.ContentLength = -1
		wg.Add(1)
		go func() { defer wg.Done(); h.ServeHTTP(httptest.NewRecorder(), r) }()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("body reads not started")
		}
	}
	// Ten configured budgets pass; both requests should have relinquished slots.
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	raw := fmt.Sprintf(`{"question":"valid query","document_ids":[%q]}`, documents.NewID())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, questionRequest(raw))
	t.Logf("third request after 10 expired budgets: status=%d, body=%s", w.Code, w.Body.String())
	if w.Code != http.StatusOK {
		t.Errorf("expired readers still hold admission: got %d, want 200", w.Code)
	}
}

func TestQuestionBodyPreservesEarlierServerReadTimeout(t *testing.T) {
	s := questionFunc(func(context.Context, string, []string) (rag.Answer, error) {
		t.Error("incomplete body reached the service")
		return rag.Answer{}, nil
	})
	srv := httptest.NewUnstartedServer(questionHandler(s, time.Second, io.Discard))
	srv.Config.ReadTimeout = 20 * time.Millisecond
	srv.Config.WriteTimeout = 2 * time.Second
	srv.Start()
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = fmt.Fprint(conn, "POST /api/v1/questions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{\"question\":")
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("earlier read timeout: status=%d elapsed=%s body=%s", res.StatusCode, elapsed, body)
	if res.StatusCode != http.StatusRequestTimeout || elapsed > 500*time.Millisecond || !strings.Contains(string(body), "request_body_timeout") {
		t.Fatalf("server ReadTimeout extended or misclassified: status=%d elapsed=%s", res.StatusCode, elapsed)
	}
}

func TestQuestionBodyCancellationStopsReadBeforeService(t *testing.T) {
	var calls atomic.Int32
	s := questionFunc(func(context.Context, string, []string) (rag.Answer, error) { calls.Add(1); return rag.Answer{}, nil })
	h := questionHandler(s, time.Second, io.Discard)
	started := make(chan struct{}, 1)
	body := &questionGatedBody{reader: strings.NewReader(questionBody()), started: started, release: make(chan struct{})}
	defer body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := questionRequest(questionBody()).WithContext(ctx)
	r.Body = body
	r.ContentLength = -1
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, r); done <- w }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	cancel()
	select {
	case w := <-done:
		if w.Code != http.StatusRequestTimeout || calls.Load() != 0 {
			t.Fatal("canceled read reached service or returned wrong status", w.Code, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock request body")
	}
}

func TestQuestionBodyWatcherStopsBeforeChatAndConnectionReuse(t *testing.T) {
	var calls atomic.Int32
	s := questionFunc(func(ctx context.Context, _ string, _ []string) (rag.Answer, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return rag.Answer{}, ctx.Err()
		}
		return rag.Answer{Answer: "answer", Sources: []rag.Source{}}, nil
	})
	srv := httptest.NewUnstartedServer(questionHandler(s, 20*time.Millisecond, io.Discard))
	srv.Config.ReadTimeout = time.Second
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	for _, want := range []int{http.StatusGatewayTimeout, http.StatusOK} {
		body := questionBody()
		if _, err := fmt.Fprintf(conn, "POST /api/v1/questions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(reader, &http.Request{Method: "POST"})
		if err != nil {
			t.Fatal("connection poisoned by a completed body watcher", err)
		}
		_, readErr := io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if readErr != nil || res.StatusCode != want || res.Close {
			t.Fatalf("keep-alive status=%d want=%d close=%v error=%v", res.StatusCode, want, res.Close, readErr)
		}
	}
}

func TestQuestionOversizedBodyDoesNotWaitForDrain(t *testing.T) {
	s := questionFunc(func(context.Context, string, []string) (rag.Answer, error) {
		t.Error("oversized body reached service")
		return rag.Answer{}, nil
	})
	srv := httptest.NewUnstartedServer(questionHandler(s, time.Second, io.Discard))
	srv.Config.ReadTimeout = 2 * time.Second
	srv.Config.WriteTimeout = 3 * time.Second
	srv.Start()
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// Stop just past the limit while retaining unread bytes in Content-Length.
	if _, err := fmt.Fprintf(conn, "POST /api/v1/questions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", maxQuestionBody+1000, strings.Repeat(" ", maxQuestionBody+1)); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); res.StatusCode != http.StatusRequestEntityTooLarge || elapsed > 500*time.Millisecond || !res.Close {
		t.Fatalf("oversized body drain blocked response: status=%d elapsed=%s close=%v", res.StatusCode, elapsed, res.Close)
	}
}
