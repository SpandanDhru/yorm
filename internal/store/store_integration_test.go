//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/db"
	"github.com/SpandanDhru/yorm/internal/db/dbtest"
	"github.com/SpandanDhru/yorm/internal/game"
)

func newStore(t *testing.T) *Postgres {
	t.Helper()
	ctx := context.Background()
	url := dbtest.StartPostgres(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

var dmMember = game.Member{UserID: "usr_dm", DisplayName: "The DM", Role: auth.RoleDM}

func ev(seq int64, p game.Payload) game.Event {
	return game.Event{
		Seq: seq, Name: p.EventName(), By: "usr_dm", Cause: "cmd_1",
		At: time.Date(2026, 10, 2, 0, 14, 15, 0, time.UTC), Data: p,
	}
}

func TestStore(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.CreateSession(ctx, Session{ID: "ses_1", Name: "Crypt", InviteCode: "abc"}, dmMember); err != nil {
		t.Fatal(err)
	}

	t.Run("session and members", func(t *testing.T) {
		got, err := s.Session(ctx, "ses_1")
		if err != nil || got.Name != "Crypt" || got.InviteCode != "abc" {
			t.Fatalf("Session = %+v, %v", got, err)
		}
		if _, err := s.Session(ctx, "ses_nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing session err = %v", err)
		}
		kai := game.Member{UserID: "usr_kai", DisplayName: "Kai", Role: auth.RolePlayer}
		if err := s.AddMember(ctx, "ses_1", kai); err != nil {
			t.Fatal(err)
		}
		for _, want := range []game.Member{dmMember, kai} {
			if got, err := s.Member(ctx, "ses_1", want.UserID); err != nil || got != want {
				t.Fatalf("Member(%s) = %+v, %v", want.UserID, got, err)
			}
		}
		if _, err := s.Member(ctx, "ses_1", "usr_ghost"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing member err = %v", err)
		}
	})

	t.Run("append and load round trip", func(t *testing.T) {
		created, err := s.LoadAfter(ctx, "ses_1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(created) != 1 || created[0].Name != "MemberJoined" || created[0].By != dmMember.UserID || created[0].Cause != "" {
			t.Fatalf("new session log = %+v, want the DM's MemberJoined", created)
		}
		evs := []game.Event{
			ev(2, game.MemberJoined{Member: game.Member{UserID: "usr_kai", DisplayName: "Kai", Role: auth.RolePlayer}}),
			ev(3, game.MapSet{Map: game.Map{ID: "map_1", ImageURL: "/uploads/m.png", Cols: 10, Rows: 8, CellFeet: 5, Terrain: game.Terrain{}}}),
		}
		if err := s.Append(ctx, "ses_1", evs); err != nil {
			t.Fatal(err)
		}
		more := []game.Event{ev(4, game.TokenPlaced{Token: game.Token{
			ID: "tok_1", Label: "Rogue", Color: "#aa0000", Pos: game.Cell{X: 1, Y: 2}, Size: 1, Controllers: []game.UserID{"usr_kai"},
		}})}
		if err := s.Append(ctx, "ses_1", more); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadAfter(ctx, "ses_1", 0)
		if err != nil {
			t.Fatal(err)
		}
		want := append(append(created, evs...), more...)
		for i := range got {
			got[i].At = got[i].At.UTC()
			want[i].At = want[i].At.UTC()
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Load =\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("stale writer gets ErrConflict and writes nothing", func(t *testing.T) {
		stale := []game.Event{ev(4, game.TokenRemoved{Token: "tok_1"}), ev(5, game.TokenRemoved{Token: "tok_1"})}
		if err := s.Append(ctx, "ses_1", stale); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
		skipAhead := []game.Event{ev(9, game.TokenRemoved{Token: "tok_1"})}
		if err := s.Append(ctx, "ses_1", skipAhead); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
		got, err := s.LoadAfter(ctx, "ses_1", 0)
		if err != nil || len(got) != 4 {
			t.Fatalf("Load after conflicts = %d events, %v; want 4", len(got), err)
		}
	})

	t.Run("load after a seq, and page through history", func(t *testing.T) {
		tail, err := s.LoadAfter(ctx, "ses_1", 2)
		if err != nil || len(tail) != 2 || tail[0].Seq != 3 {
			t.Fatalf("LoadAfter(2) = %d events from %v, %v", len(tail), tail, err)
		}
		page, err := s.Events(ctx, "ses_1", 1, 2)
		if err != nil || len(page) != 2 || page[0].Seq != 2 || page[1].Seq != 3 {
			t.Fatalf("Events(after 1, limit 2) = %+v, %v", page, err)
		}
		if none, err := s.Events(ctx, "ses_1", 4, 10); err != nil || len(none) != 0 {
			t.Fatalf("Events past the end = %+v, %v", none, err)
		}
	})

	t.Run("snapshots keep the latest three per format", func(t *testing.T) {
		if _, _, err := s.LatestSnapshot(ctx, "ses_1", 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("no snapshot yet: err = %v", err)
		}
		for seq := int64(1); seq <= 5; seq++ {
			if err := s.SaveSnapshot(ctx, "ses_1", seq, 1, []byte(`{"seq":`+strconv.FormatInt(seq, 10)+`}`)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SaveSnapshot(ctx, "ses_1", 5, 1, []byte(`{"dup":true}`)); err != nil {
			t.Fatalf("saving the same seq twice: %v", err)
		}
		seq, state, err := s.LatestSnapshot(ctx, "ses_1", 1)
		if err != nil || seq != 5 || string(state) != `{"seq": 5}` {
			t.Fatalf("latest = %d %s, %v", seq, state, err)
		}
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM snapshots WHERE session_id = 'ses_1'`).Scan(&n); err != nil || n != KeepSnapshots {
			t.Fatalf("kept %d snapshots, want %d (%v)", n, KeepSnapshots, err)
		}
		if _, _, err := s.LatestSnapshot(ctx, "ses_1", 2); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a newer format found an old snapshot: %v", err)
		}
	})

	t.Run("events are stored with their version", func(t *testing.T) {
		var v int
		if err := s.pool.QueryRow(ctx, `SELECT version FROM events WHERE session_id = 'ses_1' AND seq = 3`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("version = %d, %v", v, err)
		}
	})

	t.Run("load missing session", func(t *testing.T) {
		if _, err := s.LoadAfter(ctx, "ses_nope", 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// With group commit, appends from many sessions share transactions; each
// still succeeds or conflicts on its own.
func TestGroupCommit(t *testing.T) {
	s := newStore(t)
	s.GroupCommit(2, 16)
	t.Cleanup(s.Close)
	ctx := context.Background()
	const sessions, perSession = 20, 30
	for i := range sessions {
		id := "ses_g" + strconv.Itoa(i)
		if err := s.CreateSession(ctx, Session{ID: id, Name: id, InviteCode: "x"}, dmMember); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, sessions)
	for i := range sessions {
		go func() {
			id := "ses_g" + strconv.Itoa(i)
			for seq := int64(2); seq < 2+perSession; seq++ {
				if err := s.Append(ctx, id, []game.Event{ev(seq, game.TokenRemoved{Token: "t"})}); err != nil {
					errs <- fmt.Errorf("%s seq %d: %w", id, seq, err)
					return
				}
				// A stale write, racing alongside, must conflict without
				// disturbing the others in its batch.
				if err := s.Append(ctx, id, []game.Event{ev(seq, game.TokenRemoved{Token: "t"})}); !errors.Is(err, ErrConflict) {
					errs <- fmt.Errorf("%s stale seq %d: %w, want ErrConflict", id, seq, err)
					return
				}
			}
			errs <- nil
		}()
	}
	for range sessions {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for i := range sessions {
		evs, err := s.LoadAfter(ctx, "ses_g"+strconv.Itoa(i), 0)
		if err != nil || len(evs) != 1+perSession {
			t.Fatalf("session %d has %d events, %v; want %d", i, len(evs), err, 1+perSession)
		}
	}
}

func TestDeleteSession(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, Session{ID: "ses_del", Name: "Doomed", InviteCode: "x"}, dmMember); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "ses_del", []game.Event{ev(2, game.TokenRemoved{Token: "t"})}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(ctx, "ses_del", 2, 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "ses_del"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "events", "snapshots", "members"} {
		col := "session_id"
		if table == "sessions" {
			col = "id"
		}
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE `+col+` = 'ses_del'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s still has %d rows (%v)", table, n, err)
		}
	}
	if err := s.DeleteSession(ctx, "ses_del"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
