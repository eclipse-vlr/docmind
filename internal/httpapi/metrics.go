package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

type measurement struct {
	count   uint64
	seconds float64
}
type metricKey struct{ route, method, status string }
type httpMetrics struct {
	mu     sync.Mutex
	values map[metricKey]measurement
}

func newHTTPMetrics() *httpMetrics { return &httpMetrics{values: make(map[metricKey]measurement)} }

func (m *httpMetrics) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		out := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(out, r)
		if r.Pattern == "GET /metrics" {
			return
		}
		status := out.status
		if status == 0 {
			status = 200
		}
		method := boundedMethod(r.Method)
		key := metricKey{route: r.Pattern, method: method, status: fmt.Sprintf("%dxx", status/100)}
		m.mu.Lock()
		v := m.values[key]
		v.count++
		v.seconds += time.Since(start).Seconds()
		m.values[key] = v
		m.mu.Unlock()
	})
}

func boundedMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method
	default:
		return "OTHER"
	}
}

// ServeHTTP exports bounded route/method/status aggregates in Prometheus text format.
// No URL, document ID, question, filename or provider error becomes a label.
func (m *httpMetrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	values := make(map[metricKey]measurement, len(m.values))
	for k, v := range m.values {
		values[k] = v
	}
	m.mu.Unlock()
	keys := make([]metricKey, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintln(w, "# HELP docmind_http_requests_total Completed HTTP requests excluding metrics scrapes.\n# TYPE docmind_http_requests_total counter")
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "docmind_http_requests_total{route=%q,method=%q,status=%q} %d\n", k.route, k.method, k.status, values[k].count)
	}
	_, _ = fmt.Fprintln(w, "# HELP docmind_http_request_duration_seconds Completed HTTP request duration in seconds.\n# TYPE docmind_http_request_duration_seconds summary")
	for _, k := range keys {
		v := values[k]
		_, _ = fmt.Fprintf(w, "docmind_http_request_duration_seconds_count{route=%q,method=%q,status=%q} %d\ndocmind_http_request_duration_seconds_sum{route=%q,method=%q,status=%q} %g\n", k.route, k.method, k.status, v.count, k.route, k.method, k.status, v.seconds)
	}
}
