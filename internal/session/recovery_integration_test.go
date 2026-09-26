//go:build integration

package session

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/db"
	"github.com/SpandanDhru/yorm/internal/db/dbtest"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/store"
)

// recoveryBudget is the spec's target: a 5,000-event session restores in
// under 200 ms from its snapshot plus the events after it.
const recoveryBudget = 200 * time.Millisecond

func TestRecoveryTime(t *testing.T) {
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
	st := store.New(pool)
	dm := game.Member{UserID: "usr_dm", DisplayName: "DM", Role: auth.RoleDM}
	if err := st.CreateSession(ctx, store.Session{ID: "ses_big", Name: "Long campaign", InviteCode: "x"}, dm); err != nil {
		t.Fatal(err)
	}

	// 5,000 events: a map, some tokens, and a long evening of moves and
	// rolls, with a snapshot every 200 events as the actor would take.
	const total, every = 5000, 200
	state := game.NewState("ses_big")
	evs, _ := st.LoadAfter(ctx, "ses_big", 0)
	state.Apply(evs[0])
	at := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	var batch []game.Event
	add := func(p game.Payload) {
		ev := game.Event{Seq: state.Seq + 1, Name: p.EventName(), By: "usr_dm", Cause: "c", At: at, Data: p}
		state.Apply(ev)
		batch = append(batch, ev)
		if len(batch) == 500 || state.Seq == total {
			if err := st.Append(ctx, "ses_big", batch); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
		// Snapshots at 201, 401, ..., 4801: the last 199 events come after
		// the newest one, the most a session replays with a snapshot every 200.
		if state.Seq%every == 1 && state.Seq > 1 {
			if err := st.SaveSnapshot(ctx, "ses_big", state.Seq, snapshotFormat, encodeSnapshot(state, newAnswers(1000))); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(game.MapSet{Map: game.Map{ID: "m", Cols: 40, Rows: 30, CellFeet: 5, Terrain: game.Terrain{}}})
	for i := range 20 {
		add(game.TokenPlaced{Token: game.Token{ID: game.TokenID("tok_" + string(rune('a'+i))), Label: "T", Pos: game.Cell{X: i, Y: 0}, Size: 1, Controllers: []game.UserID{}}})
	}
	for state.Seq < total-1 { // leave the last events after the final snapshot
		i := int(state.Seq)
		if i%3 == 0 {
			add(game.DiceRolled{Expr: "1d20+5", Label: "attack"})
			continue
		}
		add(game.TokenMoved{Token: game.TokenID("tok_" + string(rune('a'+i%20))), To: game.Cell{X: i % 40, Y: i % 30}})
	}
	add(game.DiceRolled{Expr: "1d20"})

	time.Sleep(100 * time.Millisecond) // let Postgres settle after the bulk load
	m := NewManager(st, discard, DefaultOptions())
	t.Cleanup(func() { _ = m.Shutdown(ctx) })
	start := time.Now()
	a, err := m.get(ctx, "ses_big")
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restored %d events in %v from the snapshot at %d (race detector %v)", total, took, a.savedSeq.Load(), raceEnabled)

	// For comparison: replaying every event with no snapshot.
	start = time.Now()
	all, err := st.LoadAfter(ctx, "ses_big", 0)
	if err != nil {
		t.Fatal(err)
	}
	full := game.NewState("ses_big")
	for _, ev := range all {
		full.Apply(ev)
	}
	t.Logf("a full replay of %d events takes %v", len(all), time.Since(start))

	if a.state.Seq != total {
		t.Fatalf("restored to seq %d, want %d", a.state.Seq, total)
	}
	// Safe to read: the actor has no clients, commands, or timers due, so
	// it isn't touching its state.
	if !reflect.DeepEqual(a.state, state) {
		t.Fatal("restored state differs from the state that was saved")
	}
	if want := int64(total - every + 1); a.savedSeq.Load() != want {
		t.Fatalf("restored from the snapshot at %d, want %d", a.savedSeq.Load(), want)
	}
	if !raceEnabled && took > recoveryBudget {
		t.Fatalf("recovery took %v, want under %v", took, recoveryBudget)
	}
}
