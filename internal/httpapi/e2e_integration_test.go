//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/db"
	"github.com/SpandanDhru/yorm/internal/db/dbtest"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/httpapi"
	"github.com/SpandanDhru/yorm/internal/session"
	"github.com/SpandanDhru/yorm/internal/store"
	"github.com/SpandanDhru/yorm/internal/ws"
)

// server is one running yormd process: everything but the database.
type server struct {
	http     *httptest.Server
	ws       *ws.Server
	sessions *session.Manager
}

func startServer(t *testing.T, st *store.Postgres, signer *auth.Signer, uploads string) *server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(st, log, session.DefaultOptions())
	wsSrv := ws.NewServer(signer, mgr, log, ws.DefaultOptions())
	hs := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Log: log, WS: wsSrv, Health: func(context.Context) error { return nil },
		Signer: signer, Store: st, Sessions: mgr, UploadDir: uploads,
	}))
	s := &server{http: hs, ws: wsSrv, sessions: mgr}
	t.Cleanup(s.stop)
	return s
}

func (s *server) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.ws.Shutdown(ctx)
	_ = s.sessions.Shutdown(ctx)
	s.http.Close()
}

func postJSON[T any](t *testing.T, url, body string) T {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:gosec // test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var v T
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: %d %s", url, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type joinResp struct {
	Session    string `json:"session"`
	User       string `json:"user"`
	Token      string `json:"token"`
	InviteCode string `json:"invite_code"`
}

type msg struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Seq   int64           `json:"seq"`
	Name  string          `json:"name"`
	Code  string          `json:"code"`
	Data  json.RawMessage `json:"data"`
	State json.RawMessage `json:"state"`
}

type client struct {
	t *testing.T
	c *websocket.Conn
}

func connect(t *testing.T, s *server, session, token string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(s.http.URL, "http") + "/ws/sessions/" + session + "?token=" + url.QueryEscape(token)
	c, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	cl := &client{t: t, c: c}
	if m := cl.read(); m.Type != "welcome" {
		t.Fatalf("first message %+v, want welcome", m)
	}
	return cl
}

func (c *client) read() msg {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.c.Read(ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	var m msg
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func (c *client) expect(typ, name string) msg {
	c.t.Helper()
	m := c.read()
	if m.Type != typ || m.Name != name {
		c.t.Fatalf("got %+v, want %s %s", m, typ, name)
	}
	return m
}

func (c *client) write(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	if err := c.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) command(id, name string, args any) {
	c.t.Helper()
	c.write(map[string]any{"type": "command", "id": id, "name": name, "args": args})
}

func (c *client) snapshot() *game.State {
	c.t.Helper()
	c.write(map[string]string{"type": "sync"})
	m := c.expect("snapshot", "")
	var s game.State
	if err := json.Unmarshal(m.State, &s); err != nil {
		c.t.Fatal(err)
	}
	return &s
}

// TestSharedGrid is milestone 1's "done when": a DM creates a session and
// uploads a map, a player joins by invite, tokens placed and dragged show
// up live for both, and a restarted server comes back with the same state.
func TestSharedGrid(t *testing.T) {
	ctx := context.Background()
	dbURL := dbtest.StartPostgres(t)
	if err := db.Migrate(ctx, dbURL); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", auth.MinKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	uploads := t.TempDir()
	srv := startServer(t, st, signer, uploads)

	// REST: create, join, upload a map before anyone is connected.
	dmJoin := postJSON[joinResp](t, srv.http.URL+"/api/sessions", `{"name":"Crypt","display_name":"Mara"}`)
	sid := dmJoin.Session
	kaiJoin := postJSON[joinResp](t, srv.http.URL+"/api/sessions/"+sid+"/join",
		`{"code":"`+dmJoin.InviteCode+`","display_name":"Kai"}`)
	uploadMap(t, srv.http.URL+"/api/sessions/"+sid+"/maps", dmJoin.Token)

	dm := connect(t, srv, sid, dmJoin.Token)
	if s := dm.snapshot(); s.Map == nil || s.Map.Cols != 12 || !s.IsDM(game.UserID(dmJoin.User)) {
		t.Fatalf("DM's first snapshot = %+v", s)
	}
	kai := connect(t, srv, sid, kaiJoin.Token)
	if m := dm.expect("event", "MemberJoined"); !strings.Contains(string(m.Data), `"Kai"`) {
		t.Fatalf("MemberJoined = %s", m.Data)
	}
	kai.snapshot()

	dm.command("c1", "place_token", map[string]any{
		"label": "Rogue", "at": map[string]int{"x": 1, "y": 1}, "controllers": []string{kaiJoin.User},
	})
	placed := dm.expect("event", "TokenPlaced")
	dm.expect("ack", "")
	if m := kai.expect("event", "TokenPlaced"); m.Seq != placed.Seq {
		t.Fatalf("clients disagree on seq: %d vs %d", m.Seq, placed.Seq)
	}
	var tp game.TokenPlaced
	if err := json.Unmarshal(placed.Data, &tp); err != nil {
		t.Fatal(err)
	}

	// The player drags their own token; everyone sees the same event.
	kai.command("c2", "move_token", map[string]any{"token": tp.Token.ID, "to": map[string]int{"x": 7, "y": 4}})
	moved := kai.expect("event", "TokenMoved")
	kai.expect("ack", "")
	if m := dm.expect("event", "TokenMoved"); m.Seq != moved.Seq || m.Seq != placed.Seq+1 {
		t.Fatalf("TokenMoved seqs: dm %d, kai %d, want %d", m.Seq, moved.Seq, placed.Seq+1)
	}

	// Invalid moves are rejected for the sender only.
	kai.command("c3", "move_token", map[string]any{"token": tp.Token.ID, "to": map[string]int{"x": 12, "y": 0}})
	if m := kai.expect("reject", ""); m.ID != "c3" || m.Code != game.CodeInvalidTarget {
		t.Fatalf("reject = %+v", m)
	}

	before := dm.snapshot()
	if kaiView := kai.snapshot(); !reflect.DeepEqual(kaiView, before) {
		t.Fatalf("clients diverged:\ndm  %+v\nkai %+v", before, kaiView)
	}

	// Restart: a new process on the same database replays the log.
	srv.stop()
	srv2 := startServer(t, st, signer, uploads)
	after := connect(t, srv2, sid, kaiJoin.Token).snapshot()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("state after restart differs:\nbefore %+v\nafter  %+v", before, after)
	}
	if got := after.Tokens[tp.Token.ID].Pos; got != (game.Cell{X: 7, Y: 4}) {
		t.Fatalf("token at %+v after restart", got)
	}
}

func uploadMap(t *testing.T, url, token string) {
	t.Helper()
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", "crypt.png")
	_, _ = fw.Write(img.Bytes())
	_ = mw.WriteField("cols", "12")
	_ = mw.WriteField("rows", "8")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
}
