//go:build integration

package store

import (
	"context"
	"errors"
	"reflect"
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
		created, err := s.Load(ctx, "ses_1")
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
		got, err := s.Load(ctx, "ses_1")
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
		got, err := s.Load(ctx, "ses_1")
		if err != nil || len(got) != 4 {
			t.Fatalf("Load after conflicts = %d events, %v; want 4", len(got), err)
		}
	})

	t.Run("load missing session", func(t *testing.T) {
		if _, err := s.Load(ctx, "ses_nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}
