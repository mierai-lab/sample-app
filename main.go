// sample-app: GitOps 流水线的参照应用(最小的 HTTP 服务)。
//
//	GET /healthz      → ok(响应头 X-App-Version = 镜像的版本,部署后确认新版本已经在服务)
//	GET /metrics      → Prometheus metrics (requests by route × code, latency histogram)
//	GET /hello?name=x → {"message": "hello, x", "version": "…"} (lang=ja → "こんにちは、x")
//
// Fault injection (FAULT_HELLO), to show that the canary analysis aborts a bad release:
//
//	1          /hello fails with 500 (caught by the error-ratio analysis)
//	contention /hello serializes on a global lock for 20ms: fine at low traffic, collapses under load
//	           (caught only by the load test, not by the metrics of the synthetic traffic)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// contentionHold is how long FAULT_HELLO=contention holds the global lock per request.
const contentionHold = 20 * time.Millisecond

func newMux(version, fault string, m *metrics) *http.ServeMux {
	mux := http.NewServeMux()
	var global sync.Mutex
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.write(w)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-App-Version", version)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		switch fault {
		case "1":
			http.Error(w, "injected fault (FAULT_HELLO)", http.StatusInternalServerError)
			return
		case "contention":
			global.Lock()
			time.Sleep(contentionHold)
			global.Unlock()
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}
		if len(name) > 64 {
			http.Error(w, "name too long", http.StatusBadRequest)
			return
		}
		greeting := "hello from mierai-lab, "
		if r.URL.Query().Get("lang") == "ja" {
			greeting = "こんにちは、"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": greeting + name, "version": version})
	})
	return mux
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	version := envOr("APP_VERSION", "dev")
	srv := &http.Server{Addr: ":" + envOr("PORT", "8080"), Handler: func() http.Handler {
		m := newMetrics()
		return m.instrument(newMux(version, os.Getenv("FAULT_HELLO"), m))
	}(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Info("listening", "addr", srv.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
