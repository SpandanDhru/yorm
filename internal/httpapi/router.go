// Package httpapi wires HTTP routes to their handlers.
package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/SpandanDhru/yorm/internal/ws"
)

type Deps struct {
	Log    *slog.Logger
	WS     *ws.Server
	Health func(context.Context) error // usually pool.Ping
}

// NewRouter builds the HTTP handler. It does not log request URLs, because
// WebSocket URLs carry the join token.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", healthz(d.Log, d.Health))
	r.Get("/ws/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		d.WS.Serve(w, r, chi.URLParam(r, "id"))
	})
	return r
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
