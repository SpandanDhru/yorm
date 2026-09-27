// Package session runs one actor goroutine per active session. The actor
// validates commands, persists the resulting events, applies them, and
// broadcasts them to connected clients; the manager starts actors on demand
// by replaying the session's log and stops them when they go idle.
package session

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/ids"
	"github.com/SpandanDhru/yorm/internal/metrics"
	"github.com/SpandanDhru/yorm/internal/store"
)

// Store is the persistence the actors need; *store.Postgres implements it.
type Store interface {
	// LoadAfter returns the events after seq, or store.ErrNotFound.
	LoadAfter(ctx context.Context, sessionID string, after int64) ([]game.Event, error)
	Append(ctx context.Context, sessionID string, evs []game.Event) error
	Member(ctx context.Context, sessionID string, user game.UserID) (game.Member, error)
	SaveSnapshot(ctx context.Context, sessionID string, seq int64, format int, state []byte) error
	// LatestSnapshot returns the newest snapshot in format, or store.ErrNotFound.
	LatestSnapshot(ctx context.Context, sessionID string, format int) (seq int64, state []byte, err error)
}

type Options struct {
	IdleTimeout   time.Duration // stop an actor this long after its last client leaves
	AppendTimeout time.Duration
	LoadTimeout   time.Duration
	InboxSize     int

	SnapshotEvery    int           // snapshot after this many events...
	SnapshotInterval time.Duration // ...or this long, if anything changed
	SnapshotTimeout  time.Duration
	CatchupEvents    int // a client this many events behind gets a snapshot instead
	RememberCommands int // command answers kept for retries
}

func DefaultOptions() Options {
	return Options{
		IdleTimeout:      30 * time.Minute,
		AppendTimeout:    5 * time.Second,
		LoadTimeout:      10 * time.Second,
		InboxSize:        256,
		SnapshotEvery:    200,
		SnapshotInterval: 5 * time.Minute,
		SnapshotTimeout:  10 * time.Second,
		CatchupEvents:    500,
		RememberCommands: 1000,
	}
}

// ReasonDeleted is the reason Client.Close gets when the session has been
// deleted; the client should not reconnect.
const ReasonDeleted = "session deleted"

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

// load builds an actor from the latest snapshot plus the events after it.
func (m *Manager) load(id string) (*actor, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.LoadTimeout)
	defer cancel()
	start := time.Now()
	log := m.log.With("session", id)

	state, answered := game.NewState(id), newAnswers(m.opts.RememberCommands)
	snapSeq, doc, err := m.store.LatestSnapshot(ctx, id, snapshotFormat)
	switch {
	case err == nil:
		if s, a, err := decodeSnapshot(doc, m.opts.RememberCommands); err != nil {
			// Replaying from the start is slower but always right.
			log.Warn("unreadable snapshot, replaying all events", "seq", snapSeq, "err", err)
		} else {
			state, answered = s, a
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	evs, err := m.store.LoadAfter(ctx, id, state.Seq)
	if err != nil {
		return nil, err
	}
	for _, ev := range evs {
		state.Apply(ev)
	}
	answered.learn(evs)

	// Keep the last events in memory for clients catching up, including
	// ones from before the snapshot.
	r := recent{max: m.opts.CatchupEvents}
	tail := evs
	if len(evs) < r.max && state.Seq > int64(len(evs)) {
		if tail, err = m.store.LoadAfter(ctx, id, max(0, state.Seq-int64(r.max))); err != nil {
			return nil, err
		}
	}
	for _, ev := range tail {
		r.add(recentEvent{ev: ev})
	}

	metrics.SessionLoad.Observe(time.Since(start).Seconds())
	log.Info("session started", "snapshot", snapSeq, "replayed", len(evs), "seq", state.Seq, "took", time.Since(start))
	a := &actor{
		id:       id,
		log:      log,
		store:    m.store,
		opts:     m.opts,
		env:      game.Env{NewID: ids.New, Roll: roll},
		now:      time.Now,
		retire:   m.retire,
		state:    state,
		clients:  make(map[*Client]struct{}),
		answered: answered,
		recent:   r,
		inbox:    make(chan any, m.opts.InboxSize),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	a.savedSeq.Store(snapSeq)
	return a, nil
}

// roll is a fair die. crypto/rand is overkill for fairness, but it's cheap
// at dice speed and leaves nothing to argue about at the table.
func roll(sides int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(sides)))
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return int(n.Int64()) + 1
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

// Sync brings the client up to date from lastSeq: with the events it
// missed, or a snapshot if that's too many or lastSeq is 0.
func (h *Handle) Sync(ctx context.Context, lastSeq int64) error {
	return h.a.send(ctx, syncMsg{client: h.c, lastSeq: lastSeq})
}

// ViewAs shows the DM the table as the given player sees it, or as
// themselves again if user is empty. Ignored for anyone but the DM.
func (h *Handle) ViewAs(ctx context.Context, user game.UserID) error {
	return h.a.send(ctx, viewAsMsg{client: h.c, user: user})
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

// Delete stops the session's actor, if it's running, disconnecting its
// clients for good. Call it after deleting the session from the store, so
// nothing can start it again.
func (m *Manager) Delete(ctx context.Context, sessionID string) {
	m.mu.Lock()
	e := m.actors[sessionID]
	delete(m.actors, sessionID)
	m.mu.Unlock()
	if e == nil {
		return
	}
	select {
	case <-e.ready:
	case <-ctx.Done():
		return
	}
	if e.a != nil {
		_ = e.a.send(ctx, deleteMsg{})
	}
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
