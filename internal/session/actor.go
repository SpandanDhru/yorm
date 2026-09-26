package session

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/metrics"
	"github.com/SpandanDhru/yorm/internal/store"
)

// Client is one connected viewer of a session.
type Client struct {
	User game.UserID
	Role auth.Role
	// Send queues a message for the client. It must never block; returning
	// false means the client is gone or too slow and is being disconnected.
	Send func(msg []byte) bool
	// Close disconnects the client, which reconnects and resyncs.
	Close func(reason string)

	viewAs game.UserID // set when the DM looks at the table as a player
}

// Result is the outcome of a command submitted with Manager.Do.
type Result struct {
	Seq int64 // last seq after the command was applied
	Err error // a *game.Reject when the command was refused
}

// Messages in the actor's inbox. One ordered inbox, rather than a channel
// per kind, means a client that joins and then syncs is always handled in
// that order.
type (
	cmdMsg struct {
		cmd    game.Command
		client *Client       // gets ack or reject; nil for Manager.Do
		reply  chan<- Result // buffered; nil for WebSocket commands
	}
	joinMsg struct {
		client *Client
		member game.Member
		done   chan<- error // buffered
	}
	leaveMsg  struct{ client *Client }
	viewAsMsg struct {
		client *Client
		user   game.UserID // empty to go back to the DM's own view
	}
	syncMsg struct {
		client  *Client
		lastSeq int64 // the last event the client applied; 0 for none
	}
)

// actor owns all state for one session. Every change goes through its
// goroutine, so the game state needs no locks.
type actor struct {
	id     string
	log    *slog.Logger
	store  Store
	opts   Options
	env    game.Env
	now    func() time.Time
	retire func(*actor) // tells the manager to forget this actor; called on the actor goroutine

	state    *game.State
	clients  map[*Client]struct{}
	answered *answers // recent command answers, for retries
	recent   recent   // recent events, for catch-up

	// Snapshots are written in the background, one at a time. savedSeq is
	// the seq of the last one written; saving is true while one is.
	savedSeq  atomic.Int64
	saving    atomic.Bool
	snapshots sync.WaitGroup

	inbox chan any
	quit  chan struct{} // closed by Manager.Shutdown
	done  chan struct{} // closed when run returns
}

var errStopped = errors.New("session: actor stopped")

// send delivers m to the actor, or fails if the actor has stopped.
func (a *actor) send(ctx context.Context, m any) error {
	// Checked first because select picks randomly, and a stopped actor's
	// inbox usually has room.
	select {
	case <-a.done:
		return errStopped
	default:
	}
	select {
	case a.inbox <- m:
		return nil
	case <-a.done:
		return errStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *actor) run() {
	defer close(a.done)
	idle := time.NewTimer(a.opts.IdleTimeout)
	defer idle.Stop()
	tick := time.NewTicker(a.opts.SnapshotInterval)
	defer tick.Stop()
	for {
		select {
		case m := <-a.inbox:
			if stop := a.handle(m); stop {
				// Another writer owns the log, so this state may be stale:
				// don't snapshot it.
				a.retire(a)
				for c := range a.clients {
					c.Close("session restarting")
				}
				a.snapshots.Wait()
				return
			}
			if len(a.clients) == 0 {
				idle.Reset(a.opts.IdleTimeout)
			} else {
				idle.Stop()
			}
		case <-tick.C:
			if a.state.Seq > a.savedSeq.Load() {
				a.snapshot()
			}
		case <-idle.C:
			a.log.Info("session idle, stopping")
			a.retire(a)
			a.finalSnapshot()
			return
		case <-a.quit:
			a.finalSnapshot()
			return
		}
	}
}

// snapshot starts writing the current state in the background, unless a
// write is already in flight; the next trigger catches up.
func (a *actor) snapshot() {
	if !a.saving.CompareAndSwap(false, true) {
		return
	}
	seq, doc := a.state.Seq, encodeSnapshot(a.state, a.answered) // encoded here: only this goroutine may read state
	a.snapshots.Add(1)
	go func() {
		defer a.snapshots.Done()
		defer a.saving.Store(false)
		a.saveSnapshot(seq, doc)
	}()
}

func (a *actor) saveSnapshot(seq int64, doc []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), a.opts.SnapshotTimeout)
	defer cancel()
	start := time.Now()
	if err := a.store.SaveSnapshot(ctx, a.id, seq, snapshotFormat, doc); err != nil {
		metrics.Snapshots.WithLabelValues("error").Inc()
		a.log.Error("snapshot failed", "seq", seq, "err", err)
		return
	}
	metrics.Snapshots.WithLabelValues("ok").Inc()
	a.savedSeq.Store(seq)
	a.log.Debug("snapshot saved", "seq", seq, "bytes", len(doc), "took", time.Since(start))
}

// finalSnapshot saves the state before the actor stops, so the next start
// replays nothing.
func (a *actor) finalSnapshot() {
	a.snapshots.Wait()
	if a.state.Seq > a.savedSeq.Load() {
		a.saveSnapshot(a.state.Seq, encodeSnapshot(a.state, a.answered))
	}
}

// handle processes one inbox message and reports whether the actor must
// stop because another writer owns the session's log.
func (a *actor) handle(m any) (stop bool) {
	switch m := m.(type) {
	case cmdMsg:
		return a.handleCommand(m)
	case joinMsg:
		// The joiner is added after its own MemberJoined goes out: its first
		// message is the snapshot it asks for with sync, which includes it.
		var err error
		if a.state.Members[m.member.UserID] == nil {
			err = a.commit(m.member.UserID, "", game.MemberJoined{Member: m.member})
		}
		if err == nil {
			a.clients[m.client] = struct{}{}
		}
		m.done <- err
		return errors.Is(err, store.ErrConflict)
	case leaveMsg:
		delete(a.clients, m.client)
	case syncMsg:
		a.sync(m)
	case viewAsMsg:
		if a.state.IsDM(m.client.User) && (m.user == "" || (a.state.Members[m.user] != nil && !a.state.IsDM(m.user))) {
			m.client.viewAs = m.user
			a.sendView(m.client)
		}
	}
	return false
}

// viewer is who client c sees the session as.
func (a *actor) viewer(c *Client) game.Viewer {
	if c.viewAs != "" {
		return game.Viewer{User: c.viewAs}
	}
	return a.state.ViewerFor(c.User)
}

func (a *actor) sendView(c *Client) {
	c.Send(mustMarshal(snapshotMsg{Type: "snapshot", Seq: a.state.Seq, State: a.state.View(a.viewer(c))}))
}

// sync brings a client up to date: with the events it missed if they are
// few and still in memory, otherwise with a snapshot.
func (a *actor) sync(m syncMsg) {
	if m.lastSeq > 0 {
		metrics.Reconnects.Inc()
	}
	if m.lastSeq > 0 && m.lastSeq <= a.state.Seq && a.state.Seq-m.lastSeq < int64(a.opts.CatchupEvents) {
		if evs, ok := a.recent.catchUp(a.viewer(m.client), m.lastSeq, a.state.Seq); ok {
			metrics.CatchUps.WithLabelValues("events").Inc()
			m.client.Send(mustMarshal(eventsMsg{Type: "events", Seq: a.state.Seq, Events: evs}))
			return
		}
	}
	metrics.CatchUps.WithLabelValues("snapshot").Inc()
	a.sendView(m.client)
}

func (a *actor) handleCommand(m cmdMsg) (stop bool) {
	start := time.Now()
	result := "ok"
	defer func() {
		metrics.Commands.WithLabelValues(m.cmd.Name, result).Inc()
		metrics.CommandLatency.Observe(time.Since(start).Seconds())
	}()

	// A command seen before (a retry after a reconnect) gets its original
	// answer and is not applied again.
	if ans, ok := a.answered.get(m.cmd.By, m.cmd.ID); ok {
		result = "duplicate"
		a.answer(m, ans)
		return false
	}

	ans := answer{User: m.cmd.By, ID: m.cmd.ID}
	payloads, err := game.Decide(a.state, m.cmd, a.env)
	var r *game.Reject
	switch {
	case errors.As(err, &r):
		ans.Reject = r
		result = r.Code
	case err != nil:
		panic("unreachable: Decide returns only *game.Reject errors")
	default:
		if err := a.commit(m.cmd.By, m.cmd.ID, payloads...); err != nil {
			a.log.Error("append failed", "cmd", m.cmd.Name, "err", err)
			result = game.CodeUnavailable
			// Not remembered: nothing was saved, so a retry should try again.
			a.answer(m, answer{Reject: &game.Reject{Code: game.CodeUnavailable, Message: "could not save the change, try again"}})
			return errors.Is(err, store.ErrConflict)
		}
		ans.Seq = a.state.Seq
	}
	a.answered.put(ans)
	a.answer(m, ans)
	return false
}

func (a *actor) answer(m cmdMsg, ans answer) {
	if ans.Reject != nil {
		a.reply(m, Result{Err: ans.Reject}, mustMarshal(rejectMsg{Type: "reject", ID: m.cmd.ID, Reject: *ans.Reject}))
		return
	}
	a.reply(m, Result{Seq: ans.Seq}, mustMarshal(ackMsg{Type: "ack", ID: m.cmd.ID, Seq: ans.Seq}))
}

func (a *actor) reply(m cmdMsg, r Result, msg []byte) {
	if m.client != nil {
		m.client.Send(msg)
	}
	if m.reply != nil {
		m.reply <- r
	}
}

// commit persists the events, then applies and broadcasts them. Nothing is
// applied or sent unless the append succeeded, so every client sees exactly
// what is in the log.
func (a *actor) commit(by game.UserID, cause string, payloads ...game.Payload) error {
	// Postgres keeps microseconds, so do the same here: replayed state must
	// match live state exactly, and events carry this time into it.
	at := a.now().UTC().Truncate(time.Microsecond)
	evs := make([]game.Event, len(payloads))
	for i, p := range payloads {
		evs[i] = game.Event{Seq: a.state.Seq + int64(i) + 1, Name: p.EventName(), By: by, Cause: cause, At: at, Data: p}
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.opts.AppendTimeout)
	defer cancel()
	start := time.Now()
	if err := a.store.Append(ctx, a.id, evs); err != nil {
		return err
	}
	metrics.EventAppend.Observe(time.Since(start).Seconds())
	for _, ev := range evs {
		start := time.Now()
		// What each player is sent depends on whether ev changes what they
		// can see, so compare before and after.
		players := a.players()
		before := make([]game.Visible, len(players))
		for i, v := range players {
			before[i] = a.state.Visible(v)
		}
		a.state.Apply(ev)
		views := make(map[game.UserID]viewed, len(players))
		for i, v := range players {
			pev, ok := game.Deliver(a.state, v, ev, before[i])
			views[v.User] = viewed{ev: pev, fresh: !ok}
		}
		a.broadcast(ev, views)
		a.recent.add(recentEvent{ev: ev, views: views})
		metrics.Broadcast.Observe(time.Since(start).Seconds())
	}
	if a.state.Seq-a.savedSeq.Load() >= int64(a.opts.SnapshotEvery) {
		a.snapshot()
	}
	return nil
}

// players lists every member who doesn't see everything.
func (a *actor) players() []game.Viewer {
	var vs []game.Viewer
	for _, id := range slices.Sorted(maps.Keys(a.state.Members)) {
		if v := a.state.ViewerFor(id); !v.DM {
			vs = append(vs, v)
		}
	}
	return vs
}

// broadcast sends each client its version of ev: the event, its projection,
// or a fresh view if what they can see changed.
func (a *actor) broadcast(ev game.Event, views map[game.UserID]viewed) {
	raw := mustMarshal(eventMsg{Type: "event", Event: ev})
	encoded := map[game.UserID][]byte{} // one encoding per player, however many tabs they have open
	for c := range a.clients {
		v := a.viewer(c)
		if v.DM {
			c.Send(raw)
			continue
		}
		msg, ok := encoded[v.User]
		if !ok {
			if vw, known := views[v.User]; known && !vw.fresh {
				msg = mustMarshal(eventMsg{Type: "event", Event: vw.ev})
			} else {
				msg = mustMarshal(snapshotMsg{Type: "snapshot", Seq: a.state.Seq, State: a.state.View(v)})
			}
			encoded[v.User] = msg
		}
		c.Send(msg)
	}
}
