// Package bot is a scripted Yorm client for load and chaos tests. A bot
// speaks the same protocol as the browser: it keeps its own copy of the
// session state from what the server sends, syncs from its last event
// after a reconnect, and resends commands that were never answered.
package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/SpandanDhru/yorm/internal/game"
)

// Client is one bot's connection to a session.
type Client struct {
	User  game.UserID
	DM    bool
	url   string
	stats *Stats

	mu       sync.Mutex
	conn     *websocket.Conn
	state    *game.State // nil until the first snapshot
	stale    bool        // saw a gap; waiting for the sync's answer
	outbox   map[string]outgoing
	waiters  map[string]chan Answer
	verifyCh chan *game.State // set while Verify waits for a snapshot
}

type outgoing struct {
	name string
	args any
}

// Answer is how the server answered a command.
type Answer struct {
	Seq  int64
	Code string // empty if accepted
}

var errClosed = errors.New("bot: connection closed")

// NewClient prepares a client for a session; Run connects it.
func NewClient(baseURL, session, token string, user game.UserID, dm bool, stats *Stats) *Client {
	u := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws/sessions/" + url.PathEscape(session) + "?token=" + url.QueryEscape(token)
	return &Client{
		User: user, DM: dm, url: u, stats: stats,
		outbox: map[string]outgoing{}, waiters: map[string]chan Answer{},
	}
}

// Run keeps the client connected until ctx ends, reconnecting with backoff
// like the browser does.
func (c *Client) Run(ctx context.Context) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.stats.Disconnects.Add(1)
		if errors.Is(err, errSessionGone) {
			return
		}
		delay := min(10*time.Second, 250*time.Millisecond<<min(attempt, 6))
		select {
		case <-time.After(delay/2 + rand.N(delay/2+1)): //nolint:gosec // reconnect jitter, not a secret
		case <-ctx.Done():
			return
		}
	}
}

var errSessionGone = errors.New("bot: session not found")

// session runs one connection: sync, resend what's unanswered, then read.
func (c *Client) session(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.url, nil)
	cancel()
	if err != nil {
		return err
	}
	conn.SetReadLimit(4 << 20) // snapshots can be large
	defer func() { _ = conn.CloseNow() }()

	c.mu.Lock()
	c.conn = conn
	var lastSeq int64
	if c.state != nil {
		lastSeq = c.state.Seq
		c.stats.Reconnects.Add(1)
	}
	resend := make(map[string]outgoing, len(c.outbox))
	for id, o := range c.outbox {
		resend[id] = o
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	if err := c.write(ctx, conn, map[string]any{"type": "sync", "last_seq": lastSeq}); err != nil {
		return err
	}
	for id, o := range resend {
		if err := c.write(ctx, conn, map[string]any{"type": "command", "id": id, "name": o.name, "args": o.args}); err != nil {
			return err
		}
	}
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == 4404 {
				return errSessionGone
			}
			return err
		}
		c.handle(ctx, conn, b)
	}
}

func (c *Client) write(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// message is any server message.
type message struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Seq     int64           `json:"seq"`
	Name    string          `json:"name"`
	By      game.UserID     `json:"by"`
	Cause   string          `json:"cause"`
	At      time.Time       `json:"at"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`
	State   *game.State     `json:"state"`
	Events  []message       `json:"events"`
	Message string          `json:"message"`
}

func (c *Client) handle(ctx context.Context, conn *websocket.Conn, b []byte) {
	now := time.Now()
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		c.stats.Errors.Add(1)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case "snapshot":
		c.state, c.stale = m.State, false
		if c.verifyCh != nil {
			c.verifyCh <- m.State
			c.verifyCh = nil
		}
	case "event":
		c.stats.Events.Add(1)
		c.observe(m, now)
		if c.state == nil || c.stale {
			return
		}
		c.apply(ctx, conn, m)
	case "events":
		if c.state == nil {
			return
		}
		c.stale = false
		for _, e := range m.Events {
			c.apply(ctx, conn, e)
		}
		if c.state.Seq < m.Seq {
			c.stats.Gaps.Add(1)
		}
	case "ack", "reject":
		if m.Type == "reject" {
			c.stats.Rejects.Add(1)
			c.stats.rejected(m.Code)
		} else {
			c.stats.Acks.Add(1)
			c.stats.acked.Store(m.ID, m.Seq)
		}
		delete(c.outbox, m.ID)
		if w := c.waiters[m.ID]; w != nil {
			w <- Answer{Seq: m.Seq, Code: m.Code}
			delete(c.waiters, m.ID)
		}
	case "error":
		c.stats.Errors.Add(1)
	}
}

// observe records the command-to-broadcast latency of an event this client
// received, if a bot in this process sent the command.
func (c *Client) observe(m message, now time.Time) {
	if m.Cause == "" {
		return
	}
	if sent, ok := c.stats.sent.Load(m.Cause); ok {
		c.stats.latency(now.Sub(sent.(time.Time)))
	}
}

// apply applies one event in order; a gap marks the state stale and asks
// for a sync, as the browser does. Callers hold c.mu.
func (c *Client) apply(ctx context.Context, conn *websocket.Conn, m message) {
	switch {
	case m.Seq <= c.state.Seq:
		return // already have it
	case m.Seq > c.state.Seq+1:
		c.stats.Gaps.Add(1)
		c.stale = true
		go func() { _ = c.write(ctx, conn, map[string]any{"type": "sync", "last_seq": c.lastSeq()}) }()
		return
	}
	p, err := game.DecodePayload(m.Name, game.Version(m.Name), m.Data)
	if err != nil {
		c.stats.Errors.Add(1)
		return
	}
	c.state.Apply(game.Event{Seq: m.Seq, Name: m.Name, By: m.By, Cause: m.Cause, At: m.At, Data: p})
}

func (c *Client) lastSeq() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil {
		return 0
	}
	return c.state.Seq
}

var cmdCounter atomic.Int64

// Send sends a command without waiting for its answer. It is resent after
// a reconnect until answered.
func (c *Client) Send(name string, args any) string {
	id := fmt.Sprintf("%s-%d", c.User, cmdCounter.Add(1))
	c.mu.Lock()
	c.outbox[id] = outgoing{name, args}
	conn := c.conn
	c.mu.Unlock()
	c.stats.sent.Store(id, time.Now())
	c.stats.Commands.Add(1)
	if conn != nil {
		_ = c.write(context.Background(), conn, map[string]any{"type": "command", "id": id, "name": name, "args": args})
	}
	return id
}

// Do sends a command and waits for its answer.
func (c *Client) Do(ctx context.Context, name string, args any) (Answer, error) {
	ch := make(chan Answer, 1)
	id := fmt.Sprintf("%s-%d", c.User, cmdCounter.Add(1))
	c.mu.Lock()
	c.waiters[id] = ch
	c.outbox[id] = outgoing{name, args}
	conn := c.conn
	c.mu.Unlock()
	c.stats.sent.Store(id, time.Now())
	c.stats.Commands.Add(1)
	if conn != nil {
		_ = c.write(ctx, conn, map[string]any{"type": "command", "id": id, "name": name, "args": args})
	}
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// Unanswered reports how many commands are still waiting for an answer.
func (c *Client) Unanswered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.outbox)
}

// State returns a copy of the client's view, or nil before the first
// snapshot.
func (c *Client) State() *game.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil {
		return nil
	}
	b, _ := json.Marshal(c.state)
	var s game.State
	_ = json.Unmarshal(b, &s)
	return &s
}

// Ready reports whether the client has a state and is connected.
func (c *Client) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state != nil && c.conn != nil && !c.stale
}

// Verify compares the client's state, built from everything it was sent,
// with a fresh snapshot of its view from the server. Call it when the
// session is quiet. It returns both hashes.
func (c *Client) Verify(ctx context.Context) (local, server string, err error) {
	ch := make(chan *game.State, 1)
	c.mu.Lock()
	if c.state == nil || c.conn == nil {
		c.mu.Unlock()
		return "", "", errClosed
	}
	local = Hash(c.state)
	c.verifyCh = ch
	conn := c.conn
	c.mu.Unlock()
	if err := c.write(ctx, conn, map[string]any{"type": "sync", "last_seq": 0}); err != nil {
		return "", "", err
	}
	select {
	case s := <-ch:
		return local, Hash(s), nil
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// Hash is a digest of a state's JSON, which is deterministic (maps are
// encoded with sorted keys).
func Hash(s *game.State) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
