//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	st, signer, uploads := newStack(t)
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

// newStack starts Postgres and returns what a server needs besides itself,
// so a test can stop one server and start another on the same database.
func newStack(t *testing.T) (*store.Postgres, *auth.Signer, string) {
	t.Helper()
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
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", auth.MinKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	return store.New(pool), signer, t.TempDir()
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

// do sends a command and returns the events it caused, after its ack.
// Every client in others must see the same events.
func (c *client) do(name string, args any, others ...*client) []msg {
	c.t.Helper()
	id := fmt.Sprintf("c%d", time.Now().UnixNano())
	c.command(id, name, args)
	var evs []msg
	for {
		m := c.read()
		switch m.Type {
		case "event":
			evs = append(evs, m)
			continue
		case "ack":
			if m.ID != id {
				c.t.Fatalf("ack for %s, want %s", m.ID, id)
			}
		default:
			c.t.Fatalf("%s: got %+v", name, m)
		}
		break
	}
	for _, o := range others {
		for _, want := range evs {
			if got := o.read(); got.Type != "event" || got.Seq != want.Seq || got.Name != want.Name {
				c.t.Fatalf("other client got %+v, want %s at seq %d", got, want.Name, want.Seq)
			}
		}
	}
	return evs
}

func (c *client) refused(name string, args any, code string) {
	c.t.Helper()
	c.command("r", name, args)
	if m := c.read(); m.Type != "reject" || m.Code != code {
		c.t.Fatalf("%s: got %+v, want reject %s", name, m, code)
	}
}

func names(evs []msg) []string {
	var n []string
	for _, e := range evs {
		n = append(n, e.Name)
	}
	return n
}

func data[T any](t *testing.T, m msg) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(m.Data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCombat is milestone 2's "done when", scripted: two players and a
// goblin fight through a round on a map with a wall, using physical and
// server dice, and a restart loses nothing.
func TestCombat(t *testing.T) {
	st, signer, uploads := newStack(t)
	srv := startServer(t, st, signer, uploads)
	base := srv.http.URL + "/api/sessions"

	dmSeat := postJSON[joinResp](t, base, `{"name":"Goblin ambush"}`)
	sid := dmSeat.Session
	join := func(name string) joinResp {
		return postJSON[joinResp](t, base+"/"+sid+"/join", `{"code":"`+dmSeat.InviteCode+`","display_name":"`+name+`"}`)
	}
	kaiSeat, anaSeat := join("Kai"), join("Ana")

	dm := connect(t, srv, sid, dmSeat.Token)
	dm.snapshot()
	kai := connect(t, srv, sid, kaiSeat.Token)
	dm.expect("event", "MemberJoined")
	kai.snapshot()
	ana := connect(t, srv, sid, anaSeat.Token)
	dm.expect("event", "MemberJoined")
	kai.expect("event", "MemberJoined")
	ana.snapshot()
	all := []*client{dm, kai, ana}
	others := func(c *client) []*client {
		var o []*client
		for _, x := range all {
			if x != c {
				o = append(o, x)
			}
		}
		return o
	}

	// A blank 10x6 map with a wall down column 5, open at the bottom row.
	dm.do("set_map", map[string]any{"cols": 10, "rows": 6}, kai, ana)
	dm.do("paint_cells", map[string]any{"terrain": "wall", "rect": map[string]any{"from": map[string]int{"x": 5, "y": 0}, "to": map[string]int{"x": 5, "y": 4}}}, kai, ana)

	// Characters: each player makes their own PC; Ana rolls real dice.
	// Initiative ranges don't overlap where the test needs an order:
	// Kai 12 to 31 and Ana's entered 21 both beat the goblin's -9 to 10.
	kaiPC := data[game.CharacterCreated](t, kai.do("create_character", map[string]any{"name": "Kai", "max_hp": 24, "init_bonus": 11, "speed": 30}, others(kai)...)[0]).Character
	anaPC := data[game.CharacterCreated](t, ana.do("create_character", map[string]any{"name": "Ana", "max_hp": 18, "init_bonus": 1, "speed": 25, "rolls_own_dice": true}, others(ana)...)[0]).Character
	gob := data[game.CharacterCreated](t, dm.do("create_character", map[string]any{"name": "Goblin", "max_hp": 7, "ac": 15, "init_bonus": -10}, kai, ana)[0]).Character
	for i, c := range []game.Character{kaiPC, anaPC, gob} {
		dm.do("place_token", map[string]any{"actor": c.ID, "at": map[string]int{"x": i, "y": 0}}, kai, ana)
	}

	// Initiative: the server rolls for Kai and the goblin; Ana enters a 20.
	evs := dm.do("start_combat", map[string]any{}, kai, ana)
	if got := names(evs); len(got) != 1 {
		t.Fatalf("start_combat = %v; turns must wait for Ana's roll", got)
	}
	kai.refused("set_initiative", map[string]any{"actor": anaPC.ID, "roll": 20}, "forbidden")
	evs = ana.do("set_initiative", map[string]any{"actor": anaPC.ID, "roll": 20}, dm, kai)
	if got := names(evs); len(got) != 2 || got[1] != "TurnStarted" {
		t.Fatalf("set_initiative = %v", got)
	}
	// Ana and Kai could go in either order, so the test asks whose turn it
	// is rather than assuming.
	enc := dm.snapshot().Encounter
	if enc.Round != 1 || enc.Order[len(enc.Order)-1].Actor != gob.ID {
		t.Fatalf("encounter = %+v; the goblin should be last", enc)
	}
	seats := map[game.ActorID]*client{kaiPC.ID: kai, anaPC.ID: ana, gob.ID: dm}
	tokens := map[game.ActorID]string{}
	for id, tk := range dm.snapshot().Tokens {
		tokens[tk.Actor] = string(id)
	}

	// Each player's turn: move 10 ft, try to go through the wall's far
	// side beyond their movement, attack with a physical roll, end turn.
	for range 2 {
		active := dm.snapshot().Encounter.Active
		me := seats[active]
		if me == dm {
			t.Fatal("goblin went before a player")
		}
		other := kai
		if me == kai {
			other = ana
		}
		other.refused("move_token", map[string]any{"token": tokens[active], "to": map[string]int{"x": 3, "y": 3}}, "forbidden")
		pos := dm.snapshot().Tokens[game.TokenID(tokens[active])].Pos
		evs := me.do("move_token", map[string]any{"token": tokens[active], "to": map[string]int{"x": pos.X, "y": pos.Y + 2}}, others(me)...)
		if spent := data[game.MovementSpent](t, evs[1]); spent.Feet != 10 {
			t.Fatalf("spent %+v", spent)
		}
		// Across the wall is far: down to the gap and back up.
		me.refused("move_token", map[string]any{"token": tokens[active], "to": map[string]int{"x": 9, "y": 0}}, "out_of_movement")
		// An attack with advantage on real dice: 12 and 8, keep the 12.
		roll := data[game.DiceRolled](t, me.do("roll_dice", map[string]any{"text": "2d20kh1+5 = 12 8 attack"}, others(me)...)[0])
		if !roll.Physical || roll.Result.Total != 17 || roll.Label != "attack" {
			t.Fatalf("attack roll = %+v", roll)
		}
		me.refused("roll_dice", map[string]any{"text": "1d20+5 = 30"}, "invalid_expression")
		me.do("end_turn", map[string]any{}, others(me)...)
	}

	// Goblin's turn: it takes 5 of Kai's damage, the DM rolls for it, and
	// ending its turn wraps to round 2.
	s := dm.snapshot()
	if s.Encounter.Active != gob.ID {
		t.Fatalf("active = %s, want the goblin", s.Encounter.Active)
	}
	kai.refused("end_turn", map[string]any{}, "not_your_turn")
	hp := data[game.HPChanged](t, kai.do("adjust_hp", map[string]any{"actor": kaiPC.ID, "delta": -5}, dm, ana)[0])
	if hp.HP.Current != 19 {
		t.Fatalf("Kai's HP = %+v", hp.HP)
	}
	rolled := data[game.DiceRolled](t, dm.do("roll_dice", map[string]any{"text": "1d6+2 scimitar"}, kai, ana)[0])
	if rolled.Physical || rolled.Result.Total < 3 || rolled.Result.Total > 8 {
		t.Fatalf("server roll = %+v", rolled)
	}
	evs = dm.do("end_turn", map[string]any{}, kai, ana)
	if started := data[game.TurnStarted](t, evs[1]); started.Round != 2 {
		t.Fatalf("after the goblin: %+v, want round 2", started)
	}

	// Everyone agrees, and a restarted server rebuilds the same state.
	before := dm.snapshot()
	for _, c := range []*client{kai, ana} {
		if got := c.snapshot(); !reflect.DeepEqual(got, before) {
			t.Fatalf("clients diverged:\n%+v\n%+v", before, got)
		}
	}
	if len(before.Rolls) != 3 {
		t.Fatalf("rolls = %d, want 3", len(before.Rolls))
	}
	srv.stop()
	srv2 := startServer(t, st, signer, uploads)
	after := connect(t, srv2, sid, anaSeat.Token).snapshot()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("state after restart differs:\nbefore %+v\nafter  %+v", before, after)
	}
}
