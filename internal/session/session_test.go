package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/session/sessiontest"
	"github.com/SpandanDhru/yorm/internal/store"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newManager(t *testing.T, st Store, opts Options) *Manager {
	t.Helper()
	m := NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return m
}

// msg is any server-to-client message, decoded loosely.
type msg struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Seq    int64           `json:"seq"`
	Name   string          `json:"name"`
	Cause  string          `json:"cause"`
	Code   string          `json:"code"`
	Data   json.RawMessage `json:"data"`
	State  *game.State     `json:"state"`
	Events []msg           `json:"events"`
}

// fakeClient records what the actor sends to one viewer.
type fakeClient struct {
	t      *testing.T
	h      *Handle
	msgs   chan msg
	closed chan string
}

func join(t *testing.T, m *Manager, session string, user game.UserID, role auth.Role) *fakeClient {
	t.Helper()
	fc := &fakeClient{t: t, msgs: make(chan msg, 100), closed: make(chan string, 1)}
	send := func(b []byte) bool {
		var v msg
		if err := json.Unmarshal(b, &v); err != nil {
			t.Errorf("bad message %s: %v", b, err)
		}
		fc.msgs <- v
		return true
	}
	h, err := m.Join(context.Background(), session, user, role, send, func(r string) { fc.closed <- r })
	if err != nil {
		t.Fatal(err)
	}
	fc.h = h
	t.Cleanup(h.Leave)
	return fc
}

func (fc *fakeClient) next() msg {
	fc.t.Helper()
	select {
	case v := <-fc.msgs:
		return v
	case <-time.After(3 * time.Second):
		fc.t.Fatal("timed out waiting for a message")
		return msg{}
	}
}

func (fc *fakeClient) expect(typ, name string) msg {
	fc.t.Helper()
	v := fc.next()
	if v.Type != typ || v.Name != name {
		fc.t.Fatalf("got %s %s (%+v), want %s %s", v.Type, v.Name, v, typ, name)
	}
	return v
}

func (fc *fakeClient) submit(id, name, args string) {
	fc.t.Helper()
	if err := fc.h.Submit(context.Background(), game.Command{ID: id, Name: name, Args: json.RawMessage(args)}); err != nil {
		fc.t.Fatal(err)
	}
}

func (fc *fakeClient) snapshot() *game.State {
	fc.t.Helper()
	if err := fc.h.Sync(context.Background(), 0); err != nil {
		fc.t.Fatal(err)
	}
	return fc.expect("snapshot", "").State
}

func setMap(t *testing.T, m *Manager, session string, by game.UserID) {
	t.Helper()
	_, err := m.Do(context.Background(), session, game.Command{
		ID: "cmd_map", By: by, Name: "set_map", Args: json.RawMessage(`{"image_url":"/uploads/m.png","cols":10,"rows":10}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommandsBroadcastToEveryone(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())

	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	kai := join(t, m, "ses_1", "usr_kai", auth.RolePlayer)
	dm.expect("event", "MemberJoined")

	setMap(t, m, "ses_1", "usr_dm")
	dm.expect("event", "MapSet")
	kai.expect("event", "MapSet")

	dm.submit("c1", "place_token", `{"label":"Rogue","at":{"x":1,"y":1},"controllers":["usr_kai"]}`)
	placed := dm.expect("event", "TokenPlaced")
	if ack := dm.next(); ack.Type != "ack" || ack.ID != "c1" || ack.Seq != placed.Seq {
		t.Fatalf("DM got %+v, want ack for c1 at seq %d", ack, placed.Seq)
	}
	kai.expect("event", "TokenPlaced")

	var tok struct{ Token game.Token }
	if err := json.Unmarshal(placed.Data, &tok); err != nil {
		t.Fatal(err)
	}
	kai.submit("c2", "move_token", `{"token":"`+string(tok.Token.ID)+`","to":{"x":4,"y":5}}`)
	for _, c := range []*fakeClient{dm, kai} {
		ev := c.expect("event", "TokenMoved")
		if ev.Cause != "c2" || ev.Seq != placed.Seq+1 {
			t.Fatalf("TokenMoved = %+v, want cause c2 seq %d", ev, placed.Seq+1)
		}
	}
	kai.expect("ack", "")

	for _, c := range []*fakeClient{dm, kai} {
		s := c.snapshot()
		if got := s.Tokens[tok.Token.ID].Pos; got != (game.Cell{X: 4, Y: 5}) {
			t.Fatalf("snapshot token at %+v", got)
		}
	}
	if n := st.Len("ses_1"); n != 5 {
		t.Fatalf("store has %d events, want 5", n)
	}
}

func TestRejectGoesOnlyToSender(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore("ses_1"), DefaultOptions())
	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	kai := join(t, m, "ses_1", "usr_kai", auth.RolePlayer)
	dm.expect("event", "MemberJoined")

	kai.submit("c1", "remove_token", `{"token":"tok_x"}`)
	if r := kai.expect("reject", ""); r.ID != "c1" || r.Code != game.CodeForbidden {
		t.Fatalf("reject = %+v", r)
	}
	// The DM got nothing: the next thing it sees is its own snapshot.
	dm.snapshot()
}

// If the append fails, the command is rejected and nobody sees an event.
func TestFailedAppendRejectsAndBroadcastsNothing(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())
	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	setMap(t, m, "ses_1", "usr_dm")
	dm.expect("event", "MapSet")

	st.FailNextAppend(errors.New("disk full"))
	dm.submit("c1", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	if r := dm.expect("reject", ""); r.Code != game.CodeUnavailable {
		t.Fatalf("reject = %+v", r)
	}
	if s := dm.snapshot(); len(s.Tokens) != 0 || s.Seq != 2 {
		t.Fatalf("state changed after failed append: seq %d, %d tokens", s.Seq, len(s.Tokens))
	}

	// The actor keeps going: the next command gets the next seq.
	dm.submit("c2", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	if ev := dm.expect("event", "TokenPlaced"); ev.Seq != 3 {
		t.Fatalf("seq = %d, want 3", ev.Seq)
	}
}

// Losing a write race means another actor owns the log: this one
// disconnects its clients and stops, and the next join replays the log.
func TestConflictStopsActorAndRejoinReplays(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())
	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	setMap(t, m, "ses_1", "usr_dm")
	dm.expect("event", "MapSet")

	st.FailNextAppend(store.ErrConflict)
	dm.submit("c1", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	dm.expect("reject", "")
	select {
	case <-dm.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("client not disconnected after conflict")
	}
	<-dm.h.a.done
	if err := dm.h.Submit(context.Background(), game.Command{ID: "c2"}); !errors.Is(err, errStopped) {
		t.Fatalf("submit to stopped actor: %v", err)
	}

	again := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	if s := again.snapshot(); s.Seq != 2 || s.Map == nil || !s.IsDM("usr_dm") {
		t.Fatalf("replayed state = %+v", s)
	}
}

func TestStartReplaysStoredEvents(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	st.SetEvents("ses_1", []game.Event{
		{Seq: 1, Name: "MemberJoined", Data: game.MemberJoined{Member: game.Member{UserID: "usr_dm", Role: auth.RoleDM}}},
		{Seq: 2, Name: "MapSet", Data: game.MapSet{Map: game.Map{ID: "map_1", ImageURL: "/uploads/m.png", Cols: 5, Rows: 5, CellFeet: 5}}},
		{Seq: 3, Name: "TokenPlaced", Data: game.TokenPlaced{Token: game.Token{ID: "tok_1", Label: "A", Pos: game.Cell{X: 1, Y: 1}, Size: 1}}},
	})
	m := newManager(t, st, DefaultOptions())

	// A returning member causes no new MemberJoined, so the first message is the snapshot.
	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	s := dm.snapshot()
	if s.Seq != 3 || s.Tokens["tok_1"] == nil || s.Map.ID != "map_1" {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestJoinUsesStoredDisplayName(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	st.AddMember("ses_1", game.Member{UserID: "usr_kai", DisplayName: "Kai", Role: auth.RolePlayer})
	m := newManager(t, st, DefaultOptions())
	kai := join(t, m, "ses_1", "usr_kai", auth.RolePlayer)
	if got := kai.snapshot().Members["usr_kai"].DisplayName; got != "Kai" {
		t.Fatalf("display name = %q", got)
	}
}

func TestUnknownSession(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore(), DefaultOptions())
	_, err := m.Join(context.Background(), "ses_nope", "usr_dm", auth.RoleDM, func([]byte) bool { return true }, func(string) {})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if m.Active() != 0 {
		t.Fatal("failed load left an actor behind")
	}
}

func TestIdleActorStopsAndRestartsOnDemand(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	opts := DefaultOptions()
	opts.IdleTimeout = 50 * time.Millisecond
	m := newManager(t, st, opts)

	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	time.Sleep(4 * opts.IdleTimeout)
	if m.Active() != 1 {
		t.Fatal("actor stopped while a client was connected")
	}
	dm.h.Leave()
	waitFor(t, "idle actor to stop", func() bool { return m.Active() == 0 })

	setMap(t, m, "ses_1", "usr_dm") // REST command restarts it from the log
	if st.Loads() != 2 {
		t.Fatalf("loads = %d, want 2", st.Loads())
	}
}

func TestConcurrentJoinsLoadOnce(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := m.Join(context.Background(), "ses_1", game.UserID("usr_"+strconv.Itoa(i)), auth.RolePlayer,
				func([]byte) bool { return true }, func(string) {})
			if err != nil {
				t.Error(err)
				return
			}
			h.Leave()
		}()
	}
	wg.Wait()
	if st.Loads() != 1 {
		t.Fatalf("loads = %d, want 1", st.Loads())
	}
	if n := st.Len("ses_1"); n != 20 {
		t.Fatalf("%d MemberJoined events, want 20", n)
	}
}

func TestShutdownRefusesNewWork(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore("ses_1"), DefaultOptions())
	join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Do(context.Background(), "ses_1", game.Command{Name: "set_map"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Do after shutdown: %v", err)
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
