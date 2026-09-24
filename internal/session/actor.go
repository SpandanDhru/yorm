package session

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
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
	leaveMsg struct{ client *Client }
	syncMsg  struct{ client *Client }
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

	state   *game.State
	clients map[*Client]struct{}

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
	for {
		select {
		case m := <-a.inbox:
			if stop := a.handle(m); stop {
				a.retire(a)
				for c := range a.clients {
					c.Close("session restarting")
				}
				return
			}
			if len(a.clients) == 0 {
				idle.Reset(a.opts.IdleTimeout)
			} else {
				idle.Stop()
			}
		case <-idle.C:
			a.log.Info("session idle, stopping")
			a.retire(a)
			return
		case <-a.quit:
			return
		}
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
		m.client.Send(mustMarshal(snapshotMsg{Type: "snapshot", Seq: a.state.Seq, State: a.state}))
	}
	return false
}

func (a *actor) handleCommand(m cmdMsg) (stop bool) {
	payloads, err := game.Decide(a.state, m.cmd, a.env)
	if err == nil {
		err = a.commit(m.cmd.By, m.cmd.ID, payloads...)
		if err != nil {
			a.log.Error("append failed", "cmd", m.cmd.Name, "err", err)
			stop = errors.Is(err, store.ErrConflict)
			err = &game.Reject{Code: game.CodeUnavailable, Message: "could not save the change, try again"}
		}
	}

	var r *game.Reject
	switch {
	case err == nil:
		a.reply(m, Result{Seq: a.state.Seq}, mustMarshal(ackMsg{Type: "ack", ID: m.cmd.ID, Seq: a.state.Seq}))
	case errors.As(err, &r):
		a.reply(m, Result{Err: r}, mustMarshal(rejectMsg{Type: "reject", ID: m.cmd.ID, Reject: *r}))
	default:
		panic("unreachable: Decide returns only *game.Reject errors")
	}
	return stop
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
	if err := a.store.Append(ctx, a.id, evs); err != nil {
		return err
	}
	for _, ev := range evs {
		a.state.Apply(ev)
		a.broadcast(ev)
	}
	return nil
}

func (a *actor) broadcast(ev game.Event) {
	for c := range a.clients {
		if pev, ok := project(a.state, c, ev); ok {
			c.Send(mustMarshal(eventMsg{Type: "event", Event: pev}))
		}
	}
}

// project returns the version of ev that viewer may see, or ok=false to
// drop it. Until per-viewer visibility arrives, everyone sees everything.
func project(_ *game.State, _ *Client, ev game.Event) (game.Event, bool) {
	return ev, true
}
