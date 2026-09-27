// Package httpapi wires HTTP routes to their handlers.
package httpapi

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/metrics"
	"github.com/SpandanDhru/yorm/internal/ws"
)

type Deps struct {
	Log       *slog.Logger
	WS        *ws.Server
	Health    func(context.Context) error // usually pool.Ping
	Signer    *auth.Signer
	Store     Store
	Sessions  Commander
	UploadDir string // where map images are stored
	WebDir    string // built frontend to serve; empty to serve none
	Pprof     bool   // serve /debug/pprof, for profiling under load
}

// NewRouter builds the HTTP handler. It does not log request URLs, because
// WebSocket URLs carry the join token.
func NewRouter(d Deps) http.Handler {
	a := &api{Deps: d}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", healthz(d.Log, d.Health))
	// /livez only says the process is up. Hosts that probe often (Fly
	// checks every 30s) use it, so the probes don't keep a serverless
	// Postgres like Neon awake.
	r.Get("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	r.Handle("/metrics", metrics.Handler())
	if d.Pprof {
		r.Mount("/debug", middleware.Profiler())
	}
	r.Get("/ws/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		d.WS.Serve(w, r, chi.URLParam(r, "id"))
	})
	r.Route("/api", func(r chi.Router) {
		r.Post("/sessions", a.createSession)
		r.Post("/sessions/{id}/join", a.joinSession)
		r.Post("/sessions/{id}/maps", a.uploadMap)
		r.Get("/sessions/{id}/events", a.eventHistory)
		r.Post("/sessions/{id}/images", a.uploadImage)
		r.Delete("/sessions/{id}", a.deleteSession)
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	})
	r.Get("/uploads/{name}", a.serveUpload)
	r.Get("/uploads/{session}/{name}", a.serveUpload)
	if d.WebDir != "" {
		r.Get("/*", spa(d.WebDir))
	}
	return r
}

// spa serves the built frontend: real files as themselves, and index.html
// for every other path so client-side routes like /s/{id} work on reload.
func spa(dir string) http.HandlerFunc {
	root := os.DirFS(dir)
	files := http.FileServerFS(root)
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if st, err := fs.Stat(root, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // Vite puts content hashes in these names
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, root, "index.html")
	}
}

func healthz(log *slog.Logger, check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := check(ctx); err != nil {
			log.Warn("health check failed", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"unavailable"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}
}
