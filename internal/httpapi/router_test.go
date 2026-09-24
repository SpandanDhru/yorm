package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/ws"
)

func newRouter(t *testing.T, health func(context.Context) error) (http.Handler, *auth.Signer) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", auth.MinKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{Log: log, WS: ws.NewServer(signer, log, ws.DefaultOptions()), Health: health}), signer
}

func TestHealthz(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		code int
	}{
		"db up":   {nil, http.StatusOK},
		"db down": {errors.New("connection refused"), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newRouter(t, func(context.Context) error { return tt.err })
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
		})
	}
}

// The session ID in the path must reach ws.Server: a token for another
// session is refused before the upgrade.
func TestWebSocketRouteUsesPathSession(t *testing.T) {
	h, signer := newRouter(t, func(context.Context) error { return nil })
	tok, err := signer.Issue("ses_other", "usr_kai", auth.RolePlayer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws/sessions/ses_1?token="+tok, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
