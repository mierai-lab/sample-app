package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMux(t *testing.T) {
	h := newMux("abc123", "", newMetrics())
	for _, c := range []struct {
		path, wantBody string
		wantCode       int
	}{
		{"/healthz", "ok", 200},
		{"/hello", `"message":"hello from mierai-lab, world"`, 200},
		{"/hello?name=gitops", `"message":"hello, gitops"`, 200},
		{"/hello?name=gitops&lang=ja", `"message":"こんにちは、gitops"`, 200},
		{"/hello?name=" + strings.Repeat("x", 65), "too long", 400},
		{"/nope", "", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != c.wantCode || !strings.Contains(w.Body.String(), c.wantBody) {
			t.Errorf("%s: %d %q", c.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if v := w.Header().Get("X-App-Version"); v != "abc123" {
		t.Errorf("version header = %q", v)
	}
}

func TestFaultHello(t *testing.T) {
	h := newMux("abc123", "1", newMetrics())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/hello", nil))
	if w.Code != 500 {
		t.Errorf("/hello with FAULT_HELLO: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("/healthz must stay healthy: %d", w.Code)
	}
}

func TestMetrics(t *testing.T) {
	m := newMetrics()
	h := m.instrument(newMux("v", "1", m))
	for _, p := range []string{"/hello", "/hello", "/healthz", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	out := w.Body.String()
	for _, want := range []string{
		`http_requests_total{route="GET /hello",code="500"} 2`,
		`http_requests_total{route="GET /healthz",code="200"} 1`,
		`http_requests_total{route="unmatched",code="404"} 1`,
		`http_request_duration_seconds_count{route="GET /hello"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s\n%s", want, out)
		}
	}
	if strings.Contains(out, "GET /metrics") {
		t.Error("/metrics itself must not be counted")
	}
}

// FAULT_HELLO=contention serializes requests: n concurrent requests take at least n × contentionHold,
// while a single request still succeeds (the fault is invisible at low traffic).
func TestHelloContention(t *testing.T) {
	h := newMux("v", "contention", newMetrics())
	const n = 5
	start := time.Now()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/hello", nil))
			if w.Code != 200 {
				t.Errorf("/hello with contention: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < n*contentionHold {
		t.Errorf("%d concurrent requests took %v, want ≥ %v (serialized)", n, d, n*contentionHold)
	}
}
