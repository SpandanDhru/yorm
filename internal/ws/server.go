// Package ws is the connection layer. It authenticates the join token,
// upgrades to a WebSocket, and runs a reader and a writer per client with a
// bounded send buffer, so one slow client can never block the others.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/session"
)

type Options struct {
	SendBuffer      int   // messages queued per client before it is disconnected
	MaxMessageBytes int64 // larger incoming messages close the connection with 1009
	PingInterval    time.Duration
	MaxMissedPings  int
	WriteTimeout    time.Duration
	OriginPatterns  []string // extra allowed Origin hosts, e.g. "localhost:5173"
	CommandRate     float64  // commands per second a client may send, on average
	CommandBurst    int      // and in a burst
}

func DefaultOptions() Options {
	return Options{
		SendBuffer:      256,
		MaxMessageBytes: 16 << 10,
		PingInterval:    20 * time.Second,
		MaxMissedPings:  2,
		WriteTimeout:    10 * time.Second,
		CommandRate:     20,
		CommandBurst:    20,
	}
}

// Close codes in the private range, for the client to act on.
const (
	// StatusSessionNotFound tells the client to stop reconnecting.
	StatusSessionNotFound websocket.StatusCode = 4404
)

type Server struct {
	signer   *auth.Signer
	sessions *session.Manager
	log      *slog.Logger
	opts     Options

	mu     sync.Mutex
	conns  map[*conn]struct{}
	closed bool
	wg     sync.WaitGroup // one per Serve call that is past the closed check
}

func NewServer(signer *auth.Signer, sessions *session.Manager, log *slog.Logger, opts Options) *Server {
	return &Server{signer: signer, sessions: sessions, log: log, opts: opts, conns: make(map[*conn]struct{})}
}

// Serve handles GET /ws/sessions/{id}?token=... for sessionID and returns
// when the connection is closed.
func (s *Server) Serve(w http.ResponseWriter, r *http.Request, sessionID string) {
	claims, err := s.signer.Verify(r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if claims.Session != sessionID {
		http.Error(w, "token is for a different session", http.StatusForbidden)
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.opts.OriginPatterns})
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err) // Accept has already written the HTTP error
		return
	}
	wsConn.SetReadLimit(s.opts.MaxMessageBytes)

	c := newConn(wsConn, claims, s.opts.SendBuffer)
	c.limit = newBucket(s.opts.CommandRate, s.opts.CommandBurst, time.Now)
	if !s.add(c) {
		_ = wsConn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer s.remove(c)
	s.run(c)
}

func (s *Server) run(c *conn) {
	log := s.log.With("session", c.claims.Session, "user", c.claims.User)
	log.Info("client connected", "role", c.claims.Role)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop(s.opts.WriteTimeout) }()
	go func() { defer wg.Done(); c.pingLoop(s.opts.PingInterval, s.opts.MaxMissedPings) }()

	c.enqueue(welcome(c.claims))
	err := s.play(c)
	c.fail(websocket.StatusNormalClosure, "")
	wg.Wait()

	code, reason := c.closeReason()
	log.Info("client disconnected", "code", code, "reason", reason, "err", err)
}

// play joins the client to its session and routes its messages there until
// the connection ends.
func (s *Server) play(c *conn) error {
	ctx, cancel := context.WithTimeout(c.ctx, s.opts.WriteTimeout)
	seat, err := s.sessions.Join(ctx, c.claims.Session, game.UserID(c.claims.User), c.claims.Role,
		c.enqueue, func(reason string) {
			if reason == session.ReasonDeleted {
				c.fail(StatusSessionNotFound, reason) // don't come back
				return
			}
			c.fail(websocket.StatusServiceRestart, reason)
		})
	cancel()
	if errors.Is(err, session.ErrNotFound) {
		c.fail(StatusSessionNotFound, "session not found")
		return err
	}
	if err != nil {
		c.fail(websocket.StatusInternalError, "could not join session")
		return err
	}
	defer seat.Leave()
	return c.readLoop(func(c *conn, msg []byte) { s.handleMessage(c, seat, msg) })
}

// clientMsg is any client-to-server message.
type clientMsg struct {
	Type string          `json:"type"` // ping, sync, or command
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	// LastSeq, on sync, is the last event the client applied.
	LastSeq int64 `json:"last_seq"`
	// User, on view_as, is the player whose view the DM wants.
	User string `json:"user"`
}

const maxCommandIDLen = 64

func (s *Server) handleMessage(c *conn, seat *session.Handle, msg []byte) {
	var m clientMsg
	if err := json.Unmarshal(msg, &m); err != nil {
		c.enqueue(errorMsg("malformed message"))
		return
	}
	var err error
	switch m.Type {
	case "ping":
		c.enqueue(pongMsg)
	case "sync":
		err = seat.Sync(c.ctx, m.LastSeq)
	case "view_as":
		err = seat.ViewAs(c.ctx, game.UserID(m.User))
	case "command":
		if m.ID == "" || len(m.ID) > maxCommandIDLen {
			c.enqueue(errorMsg("command id must be 1 to 64 characters"))
			return
		}
		if !c.limit.take() {
			c.enqueue(rateLimitedMsg(m.ID))
			return
		}
		err = seat.Submit(c.ctx, game.Command{ID: m.ID, Name: m.Name, Args: m.Args})
	default:
		c.enqueue(errorMsg("unknown message type " + m.Type))
	}
	if err != nil {
		// The session actor stopped; reconnecting starts a fresh one.
		c.fail(websocket.StatusServiceRestart, "session restarting")
	}
}

func rateLimitedMsg(id string) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}{"reject", id, game.CodeRateLimited, "too many commands; slow down"})
	return b
}

func errorMsg(message string) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{"error", message})
	return b
}

var pongMsg = []byte(`{"type":"pong"}`)

type welcomeMsg struct {
	Type    string    `json:"type"`
	Session string    `json:"session"`
	User    string    `json:"user"`
	Role    auth.Role `json:"role"`
	Caps    []string  `json:"caps"`
}

func welcome(cl auth.Claims) []byte {
	b, err := json.Marshal(welcomeMsg{
		Type: "welcome", Session: cl.Session, User: cl.User, Role: cl.Role, Caps: cl.Role.DefaultCaps(),
	})
	if err != nil {
		panic(err) // unreachable: the struct only holds strings
	}
	return b
}

func (s *Server) add(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) remove(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

// ConnCount reports how many clients are connected.
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Shutdown refuses new connections, closes existing ones with 1001, and
// waits for their goroutines to finish. http.Server.Shutdown does not track
// hijacked connections, so callers must call this too.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		c.fail(websocket.StatusGoingAway, "server shutting down")
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
