package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/session/sessiontest"
)

// table starts a session with a map and a DM, and returns a way to make
// n token placements (one event each).
func table(t *testing.T, m *Manager) (dm *fakeClient, place func(n int)) {
	t.Helper()
	dm = join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	dm.snapshot()
	setMap(t, m, "ses_1", "usr_dm")
	dm.expect("event", "MapSet")
	i := 0
	return dm, func(n int) {
		t.Helper()
		for range n {
			i++
			id := "p" + strconv.Itoa(i)
			dm.submit(id, "place_token", `{"label":"T`+strconv.Itoa(i)+`","at":{"x":0,"y":0}}`)
			dm.expect("event", "TokenPlaced")
			dm.expect("ack", "")
		}
	}
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func shutdown(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func decodeDoc(t *testing.T, b []byte) snapshotDoc {
	t.Helper()
	var d snapshotDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSnapshotEveryNEvents(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	opts := DefaultOptions()
	opts.SnapshotEvery = 5
	m := newManager(t, st, opts)
	dm, place := table(t, m) // seq 2

	place(9) // seq 11
	waitFor(t, "two snapshots", func() bool { return len(st.Snapshots("ses_1")) >= 2 })
	snaps := st.Snapshots("ses_1")
	for _, snap := range snaps {
		if snap.Format != snapshotFormat {
			t.Fatalf("format %d", snap.Format)
		}
		doc := decodeDoc(t, snap.State)
		if doc.State.Seq != snap.Seq {
			t.Fatalf("snapshot at %d holds state at %d", snap.Seq, doc.State.Seq)
		}
	}
	if snaps[0].Seq < 5 || snaps[1].Seq < 10 {
		t.Fatalf("snapshots at %d and %d, want one per 5 events", snaps[0].Seq, snaps[1].Seq)
	}
	// The latest snapshot matches the live state at its seq.
	live := dm.snapshot()
	last := decodeDoc(t, snaps[len(snaps)-1].State).State
	if last.Seq == live.Seq && !reflect.DeepEqual(last, live) {
		t.Fatalf("snapshot differs from live state:\n%+v\n%+v", last, live)
	}
}

func TestSnapshotOnATimer(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	opts := DefaultOptions()
	opts.SnapshotInterval = 30 * time.Millisecond
	m := newManager(t, st, opts)
	_, place := table(t, m)
	place(1)
	waitFor(t, "a timed snapshot", func() bool { return len(st.Snapshots("ses_1")) > 0 })
	n := len(st.Snapshots("ses_1"))
	time.Sleep(5 * opts.SnapshotInterval)
	if got := len(st.Snapshots("ses_1")); got != n {
		t.Fatalf("snapshotted %d more times with nothing new", got-n)
	}
}

// A clean shutdown saves a final snapshot, so the next start replays
// nothing and ends up with the same state.
func TestRestartFromSnapshot(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m1 := NewManager(st, discard, DefaultOptions())
	dm, place := table(t, m1)
	place(3)
	before := dm.snapshot()
	shutdown(t, m1)

	snaps := st.Snapshots("ses_1")
	if len(snaps) != 1 || snaps[0].Seq != before.Seq {
		t.Fatalf("snapshots = %+v, want one at seq %d", snaps, before.Seq)
	}
	m2 := newManager(t, st, DefaultOptions())
	after := join(t, m2, "ses_1", "usr_dm", auth.RoleDM).snapshot()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("restored state differs:\n%+v\n%+v", before, after)
	}
}

// A crash leaves events after the last snapshot; they are replayed on top.
func TestRestartReplaysEventsAfterTheSnapshot(t *testing.T) {
	live := sessiontest.NewMemStore("ses_1")
	opts := DefaultOptions()
	opts.SnapshotEvery = 4
	m1 := NewManager(live, discard, opts)
	dm, place := table(t, m1)
	place(5) // seq 7; a snapshot at 4 or so
	waitFor(t, "a snapshot", func() bool { return len(live.Snapshots("ses_1")) > 0 })
	before := dm.snapshot()
	early := live.Snapshots("ses_1")[0]
	shutdown(t, m1)

	// The "crashed" store: every event, but only the early snapshot.
	crashed := sessiontest.NewMemStore()
	evs, _ := live.LoadAfter(context.Background(), "ses_1", 0)
	crashed.SetEvents("ses_1", evs)
	_ = crashed.SaveSnapshot(context.Background(), "ses_1", early.Seq, early.Format, early.State)

	m2 := newManager(t, crashed, DefaultOptions())
	after := join(t, m2, "ses_1", "usr_dm", auth.RoleDM).snapshot()
	if early.Seq >= before.Seq || !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot at %d, live at %d; restored:\n%+v\nwant\n%+v", early.Seq, before.Seq, after, before)
	}
}

// A snapshot that can't be read is skipped, and the session replays every
// event instead.
func TestUnreadableSnapshotFallsBackToReplay(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m1 := NewManager(st, discard, DefaultOptions())
	dm, place := table(t, m1)
	place(2)
	before := dm.snapshot()
	shutdown(t, m1)

	_ = st.SaveSnapshot(context.Background(), "ses_1", before.Seq+100, snapshotFormat, []byte(`{"state":{"seq":"not a number"}}`))
	m2 := newManager(t, st, DefaultOptions())
	if after := join(t, m2, "ses_1", "usr_dm", auth.RoleDM).snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("state after fallback differs:\n%+v\n%+v", after, before)
	}
}

func TestCatchUp(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	opts := DefaultOptions()
	opts.CatchupEvents = 5
	m := newManager(t, st, opts)
	dm, place := table(t, m)
	seen := dm.snapshot().Seq
	place(3)

	sync := func(last int64) msg {
		t.Helper()
		if err := dm.h.Sync(context.Background(), last); err != nil {
			t.Fatal(err)
		}
		return dm.next()
	}

	// A few behind: just the missed events, in order, in one message.
	got := sync(seen)
	if got.Type != "events" || got.Seq != seen+3 || len(got.Events) != 3 || got.Events[0].Seq != seen+1 {
		t.Fatalf("catch-up = %+v", got)
	}
	// Up to date: an empty batch.
	if got := sync(seen + 3); got.Type != "events" || len(got.Events) != 0 {
		t.Fatalf("up to date = %+v", got)
	}
	// Too far behind, from nothing, or ahead of the server: a snapshot.
	for _, last := range []int64{seen + 3 - 5, 0, seen + 100} {
		if got := sync(last); got.Type != "snapshot" {
			t.Fatalf("sync from %d = %s, want snapshot", last, got.Type)
		}
	}
}

// After a restart, catch-up still works for events from before it.
func TestCatchUpAcrossRestart(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m1 := NewManager(st, discard, DefaultOptions())
	dm, place := table(t, m1)
	seen := dm.snapshot().Seq
	place(2)
	shutdown(t, m1)

	m2 := newManager(t, st, DefaultOptions())
	c := join(t, m2, "ses_1", "usr_dm", auth.RoleDM)
	if err := c.h.Sync(context.Background(), seen); err != nil {
		t.Fatal(err)
	}
	if got := c.next(); got.Type != "events" || len(got.Events) != 2 {
		t.Fatalf("catch-up after restart = %+v", got)
	}
}

func TestRetriedCommandsApplyOnce(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())
	dm, _ := table(t, m)
	kai := join(t, m, "ses_1", "usr_kai", auth.RolePlayer)
	dm.expect("event", "MemberJoined")
	kai.snapshot()

	dm.submit("same", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	placed := dm.expect("event", "TokenPlaced")
	first := dm.expect("ack", "")
	kai.expect("event", "TokenPlaced")

	dm.submit("same", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	if again := dm.next(); again.Type != "ack" || again.Seq != first.Seq || again.Seq != placed.Seq {
		t.Fatalf("retry = %+v, want the original ack at seq %d", again, first.Seq)
	}
	if n := st.Len("ses_1"); int64(n) != placed.Seq {
		t.Fatalf("store has %d events, want %d: the retry was applied", n, placed.Seq)
	}

	// A rejected command is rejected again the same way.
	kai.submit("nope", "remove_token", `{"token":"tok_x"}`)
	kai.expect("reject", "")
	kai.submit("nope", "remove_token", `{"token":"tok_x"}`)
	if r := kai.expect("reject", ""); r.Code != game.CodeForbidden {
		t.Fatalf("retried reject = %+v", r)
	}

	// IDs are per user: the same ID from someone else is a new command.
	kai.submit("same", "roll_dice", `{"text":"1d20"}`)
	kai.expect("event", "DiceRolled")
	kai.expect("ack", "")
}

// Answers survive a restart, through the snapshot and through the events
// replayed after it.
func TestRetriesStaySafeAcrossRestart(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m1 := NewManager(st, discard, DefaultOptions())
	dm, _ := table(t, m1)
	dm.submit("before", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	dm.expect("event", "TokenPlaced")
	dm.expect("ack", "")
	shutdown(t, m1) // snapshot includes "before"

	m2 := newManager(t, st, DefaultOptions())
	dm2 := join(t, m2, "ses_1", "usr_dm", auth.RoleDM)
	n := st.Len("ses_1")
	dm2.submit("before", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	if got := dm2.next(); got.Type != "ack" {
		t.Fatalf("retry after restart = %+v", got)
	}
	if st.Len("ses_1") != n {
		t.Fatal("retry after restart was applied again")
	}
}

// A command that failed to save is not remembered, so a retry tries again.
func TestFailedSaveCanBeRetried(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())
	dm, _ := table(t, m)
	st.FailNextAppend(errors.New("disk full"))
	dm.submit("c1", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	if r := dm.expect("reject", ""); r.Code != game.CodeUnavailable {
		t.Fatalf("reject = %+v", r)
	}
	dm.submit("c1", "place_token", `{"label":"A","at":{"x":0,"y":0}}`)
	dm.expect("event", "TokenPlaced")
	dm.expect("ack", "")
}

func TestAnswersForgetTheOldest(t *testing.T) {
	a := newAnswers(3)
	for i := range 5 {
		a.put(answer{User: "u", ID: strconv.Itoa(i), Seq: int64(i)})
	}
	a.put(answer{User: "u", ID: "4", Seq: 40}) // an update doesn't grow the list
	if _, ok := a.get("u", "1"); ok {
		t.Fatal("kept an answer past the limit")
	}
	if got := a.list(); len(got) != 3 || got[0].ID != "2" || got[2].Seq != 40 {
		t.Fatalf("list = %+v", got)
	}
}

func TestDeleteDisconnectsForGood(t *testing.T) {
	st := sessiontest.NewMemStore("ses_1")
	m := newManager(t, st, DefaultOptions())
	dm := join(t, m, "ses_1", "usr_dm", auth.RoleDM)
	dm.snapshot()
	m.Delete(context.Background(), "ses_1")
	select {
	case reason := <-dm.closed:
		if reason != ReasonDeleted {
			t.Fatalf("closed with %q", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client not disconnected")
	}
	waitFor(t, "the actor to stop", func() bool { return m.Active() == 0 })
	m.Delete(context.Background(), "ses_1") // deleting again is harmless
}
