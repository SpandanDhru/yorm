package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/SpandanDhru/yorm/internal/auth"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type harness struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	signer *auth.Signer
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", auth.MinKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(signer, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		srv.Serve(w, r, r.PathValue("id"))
	})
	hs := httptest.NewServer(mux)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		hs.Close()
	})
	return &harness{t: t, srv: srv, http: hs, signer: signer}
}

func (h *harness) token(session, user string, role auth.Role) string {
	h.t.Helper()
	tok, err := h.signer.Issue(session, user, role, time.Hour)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// testCtx bounds a test's client operations and ends with the test.
func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (h *harness) dial(session, token string) (*websocket.Conn, *http.Response, error) {
	u := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/sessions/" + session + "?token=" + url.QueryEscape(token)
	c, resp, err := websocket.Dial(testCtx(h.t), u, &websocket.DialOptions{HTTPClient: h.http.Client()})
	if c != nil {
		h.t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

// connect dials and consumes the welcome message.
func (h *harness) connect(session, user string, role auth.Role) (*websocket.Conn, welcomeMsg) {
	h.t.Helper()
	c, _, err := h.dial(session, h.token(session, user, role))
	if err != nil {
		h.t.Fatal(err)
	}
	var w welcomeMsg
	readJSON(h.t, c, &w)
	return c, w
}

func readJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	_, b, err := c.Read(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWelcomeThenEcho(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	c, w := h.connect("ses_1", "usr_dm", auth.RoleDM)

	if w.Type != "welcome" || w.Session != "ses_1" || w.User != "usr_dm" || w.Role != auth.RoleDM {
		t.Fatalf("unexpected welcome %+v", w)
	}
	if !slices.Contains(w.Caps, "session:admin") {
		t.Fatalf("DM caps missing session:admin: %v", w.Caps)
	}

	ctx := testCtx(t)
	msg := []byte(`{"type":"hello","n":1}`)
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	_, got, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo = %s, want %s", got, msg)
	}
}

func TestPlayerWelcomeHasNoAdminCaps(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	_, w := h.connect("ses_1", "usr_kai", auth.RolePlayer)
	if w.Role != auth.RolePlayer || slices.Contains(w.Caps, "session:admin") || slices.Contains(w.Caps, "view:all") {
		t.Fatalf("unexpected player welcome %+v", w)
	}
}

func TestPingPong(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	c, _ := h.connect("ses_1", "usr_kai", auth.RolePlayer)
	if err := c.Write(testCtx(t), websocket.MessageText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	var pong struct{ Type string }
	readJSON(t, c, &pong)
	if pong.Type != "pong" {
		t.Fatalf("got %q, want pong", pong.Type)
	}
}

func TestRejectsBadTokens(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	expired, err := h.signer.Sign(auth.Claims{
		Session: "ses_1", User: "usr_kai", Role: auth.RolePlayer, Expires: time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"garbage", "not-a-token", http.StatusUnauthorized},
		{"expired", expired, http.StatusUnauthorized},
		{"other session", h.token("ses_2", "usr_kai", auth.RolePlayer), http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, resp, err := h.dial("ses_1", tt.token)
			if err == nil {
				t.Fatal("dial succeeded, want rejection")
			}
			if resp == nil || resp.StatusCode != tt.want {
				t.Fatalf("resp = %v, want status %d", resp, tt.want)
			}
		})
	}
	if n := h.srv.ConnCount(); n != 0 {
		t.Fatalf("ConnCount = %d after rejected dials", n)
	}
}

func TestOversizedMessageClosesWith1009(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	c, _ := h.connect("ses_1", "usr_kai", auth.RolePlayer)
	ctx := testCtx(t)
	// Only slightly over the limit, so the server has read nearly all of it
	// before closing. Unread data would make the kernel send a TCP reset
	// instead of letting the close frame arrive.
	big := `{"type":"x","pad":"` + strings.Repeat("a", 17<<10) + `"}`
	_ = c.Write(ctx, websocket.MessageText, []byte(big)) // may fail if the server closes first
	_, _, err := c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %v (err %v), want 1009", got, err)
	}
	waitFor(t, "server to drop the connection", func() bool { return h.srv.ConnCount() == 0 })
}

func TestBinaryMessageClosesWith1003(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	c, _ := h.connect("ses_1", "usr_kai", auth.RolePlayer)
	ctx := testCtx(t)
	if err := c.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusUnsupportedData {
		t.Fatalf("close status = %v (err %v), want 1003", got, err)
	}
}

func TestFullSendBufferDisconnectsWithoutBlocking(t *testing.T) {
	c := newConn(nil, auth.Claims{}, 2)
	done := make(chan []bool)
	go func() {
		done <- []bool{c.enqueue([]byte("1")), c.enqueue([]byte("2")), c.enqueue([]byte("3"))}
	}()
	select {
	case got := <-done:
		if !slices.Equal(got, []bool{true, true, false}) {
			t.Fatalf("enqueue results = %v, want [true true false]", got)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked on a full buffer")
	}
	if code, reason := c.closeReason(); code != websocket.StatusTryAgainLater {
		t.Fatalf("close = %v %q, want 1013", code, reason)
	}
	if c.enqueue([]byte("4")) {
		t.Fatal("enqueue succeeded after the connection failed")
	}
}

func TestUnresponsiveClientIsDropped(t *testing.T) {
	opts := DefaultOptions()
	opts.PingInterval = 50 * time.Millisecond
	h := newHarness(t, opts)

	// After the welcome this client never reads again, so it never answers pings.
	h.connect("ses_1", "usr_kai", auth.RolePlayer)
	if n := h.srv.ConnCount(); n != 1 {
		t.Fatalf("ConnCount = %d, want 1", n)
	}
	waitFor(t, "unresponsive client to be dropped", func() bool { return h.srv.ConnCount() == 0 })
}

func TestResponsiveClientStaysConnected(t *testing.T) {
	opts := DefaultOptions()
	opts.PingInterval = 50 * time.Millisecond
	h := newHarness(t, opts)

	c, _ := h.connect("ses_1", "usr_kai", auth.RolePlayer)
	c.CloseRead(testCtx(t)) // keeps reading in the background, which answers pings
	time.Sleep(10 * opts.PingInterval)
	if n := h.srv.ConnCount(); n != 1 {
		t.Fatalf("ConnCount = %d, want 1", n)
	}
}

func TestShutdownClosesClientsAndRefusesNewOnes(t *testing.T) {
	h := newHarness(t, DefaultOptions())
	errs := make(chan error, 3)
	for _, u := range []string{"usr_dm", "usr_kai", "usr_ana"} {
		c, _ := h.connect("ses_1", u, auth.RolePlayer)
		go func() {
			_, _, err := c.Read(context.Background())
			errs <- err
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got := websocket.CloseStatus(<-errs); got != websocket.StatusGoingAway {
			t.Fatalf("close status = %v, want 1001", got)
		}
	}
	if n := h.srv.ConnCount(); n != 0 {
		t.Fatalf("ConnCount = %d after shutdown", n)
	}

	_, resp, err := h.dial("ses_1", h.token("ses_1", "usr_late", auth.RolePlayer))
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after shutdown: resp %v err %v, want 503", resp, err)
	}
}
