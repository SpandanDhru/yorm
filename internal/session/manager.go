// Package session runs one actor goroutine per active session. The actor
// validates commands, persists the resulting events, applies them, and
// broadcasts them to connected clients; the manager starts actors on demand
// by replaying the session's log and stops them when they go idle.
package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/ids"
	"github.com/SpandanDhru/yorm/internal/store"
)

// Store is the persistence the actors need; *store.Postgres implements it.
type Store interface {
	Load(ctx context.Context, sessionID string) ([]game.Event, error)
	Append(ctx context.Context, sessionID string, evs []game.Event) error
	Member(ctx context.Context, sessionID string, user game.UserID) (game.Member, error)
}

type Options struct {
	IdleTimeout   time.Duration // stop an actor this long after its last client leaves
	AppendTimeout time.Duration
	LoadTimeout   time.Duration
	InboxSize     int
}

func DefaultOptions() Options {
	return Options{
		IdleTimeout:   30 * time.Minute,
		AppendTimeout: 5 * time.Second,
		LoadTimeout:   10 * time.Second,
		InboxSize:     256,
	}
}

var (
	// ErrNotFound means the session does not exist.
	ErrNotFound = store.ErrNotFound
	ErrClosed   = errors.New("session: manager shut down")
)

// A started actor can stop at any moment (idle, or a lost write race), so
// callers that find it stopped look it up again, which starts a fresh one.
const maxAttempts = 3

type Manager struct {
	store Store
	log   *slog.Logger
	opts  Options

	mu     sync.Mutex
	actors map[string]*entry
	closed bool
	wg     sync.WaitGroup // one per running actor
}

// entry lets concurrent callers wait for one load of a session instead of
// each replaying it.
type entry struct {
	ready chan struct{} // closed once a or err is set
	a     *actor
	err   error
}

func NewManager(st Store, log *slog.Logger, opts Options) *Manager {
	return &Manager{store: st, log: log, opts: opts, actors: make(map[string]*entry)}
}

// get returns the running actor for the session, starting it if needed.
func (m *Manager) get(ctx context.Context, id string) (*actor, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	e, ok := m.actors[id]
	if !ok {
		e = &entry{ready: make(chan struct{})}
		m.actors[id] = e
		m.mu.Unlock()
		a, err := m.load(id) // slow, so not under the lock

		m.mu.Lock()
		if err == nil && m.closed {
			err = ErrClosed
		}
		if err != nil {
			delete(m.actors, id)
			e.err = err
		} else {
			e.a = a
			m.wg.Add(1)
			go func() { defer m.wg.Done(); a.run() }()
		}
		close(e.ready)
	}
	m.mu.Unlock()

	select {
	case <-e.ready:
		return e.a, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// load builds an actor by replaying the session's events.
func (m *Manager) load(id string) (*actor, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.LoadTimeout)
	defer cancel()
	start := time.Now()
	evs, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	state := game.NewState(id)
	for _, ev := range evs {
		state.Apply(ev)
	}
	log := m.log.With("session", id)
	log.Info("session started", "events", len(evs), "seq", state.Seq, "replay", time.Since(start))
	return &actor{
		id:      id,
		log:     log,
		store:   m.store,
		opts:    m.opts,
		env:     game.Env{NewID: ids.New},
		now:     time.Now,
		retire:  m.retire,
		state:   state,
		clients: make(map[*Client]struct{}),
		inbox:   make(chan any, m.opts.InboxSize),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

func (m *Manager) retire(a *actor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.actors[a.id]; e != nil && e.a == a {
		delete(m.actors, a.id)
	}
}

// Handle is a client's membership in a running session.
type Handle struct {
	a *actor
	c *Client
}

// Join connects a client to the session. The result of each command the
// client submits, and every event, is delivered through send.
func (m *Manager) Join(ctx context.Context, sessionID string, user game.UserID, role auth.Role,
	send func([]byte) bool, closeFn func(string)) (*Handle, error) {
	member, err := m.store.Member(ctx, sessionID, user)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Tokens are only issued by us, so a valid one is a real member even
		// without a members row (e.g. one minted with yormd mint-token).
		member = game.Member{UserID: user, DisplayName: string(user)}
	case err != nil:
		return nil, err
	}
	member.Role = role // the signed token is the authority on role

	c := &Client{User: user, Role: role, Send: send, Close: closeFn}
	for range maxAttempts {
		a, err := m.get(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		done := make(chan error, 1)
		if err := a.send(ctx, joinMsg{client: c, member: member, done: done}); errors.Is(err, errStopped) {
			continue
		} else if err != nil {
			return nil, err
		}
		// Not bounded by ctx: once queued, the join will be processed, and
		// abandoning it would leave a client the actor never hears leave.
		select {
		case err := <-done:
			return m.joined(a, c, err)
		case <-a.done:
			select {
			case err := <-done: // processed just before stopping
				return m.joined(a, c, err)
			default:
				continue
			}
		}
	}
	return nil, errStopped
}

func (m *Manager) joined(a *actor, c *Client, err error) (*Handle, error) {
	if err != nil {
		return nil, err
	}
	return &Handle{a: a, c: c}, nil
}

// Submit queues a command from this client. Its ack or reject arrives
// through the client's send function. An error means the session actor has
// stopped and the client should reconnect.
func (h *Handle) Submit(ctx context.Context, cmd game.Command) error {
	cmd.By = h.c.User
	return h.a.send(ctx, cmdMsg{cmd: cmd, client: h.c})
}

// Sync asks for a snapshot of the current state.
func (h *Handle) Sync(ctx context.Context) error {
	return h.a.send(ctx, syncMsg{client: h.c})
}

// Leave disconnects the client from the session.
func (h *Handle) Leave() {
	_ = h.a.send(context.Background(), leaveMsg{client: h.c})
}

// Do runs a command on behalf of user outside any WebSocket, for the REST
// API. The error is a *game.Reject if the command was refused.
func (m *Manager) Do(ctx context.Context, sessionID string, cmd game.Command) (int64, error) {
	for range maxAttempts {
		a, err := m.get(ctx, sessionID)
		if err != nil {
			return 0, err
		}
		reply := make(chan Result, 1)
		if err := a.send(ctx, cmdMsg{cmd: cmd, reply: reply}); errors.Is(err, errStopped) {
			continue
		} else if err != nil {
			return 0, err
		}
		select {
		case r := <-reply:
			return r.Seq, r.Err
		case <-a.done:
			select {
			case r := <-reply:
				return r.Seq, r.Err
			default:
				continue // never processed, so safe to retry
			}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return 0, errStopped
}

// Active reports how many session actors are running.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.actors)
}

// Shutdown stops every actor and waits for them to exit. Call it after the
// WebSocket server has shut down, so no client is left talking to a stopped
// actor.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		for _, e := range m.actors {
			if e.a != nil {
				close(e.a.quit)
			}
		}
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
