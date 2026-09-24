// Package ws is the connection layer. It authenticates the join token,
// upgrades to a WebSocket, and runs a reader and a writer per client with a
// bounded send buffer, so one slow client can never block the others.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/SpandanDhru/yorm/internal/auth"
)

type Options struct {
	SendBuffer      int   // messages queued per client before it is disconnected
	MaxMessageBytes int64 // larger incoming messages close the connection with 1009
	PingInterval    time.Duration
	MaxMissedPings  int
	WriteTimeout    time.Duration
	OriginPatterns  []string // extra allowed Origin hosts, e.g. "localhost:5173"
}

func DefaultOptions() Options {
	return Options{
		SendBuffer:      256,
		MaxMessageBytes: 16 << 10,
		PingInterval:    20 * time.Second,
		MaxMissedPings:  2,
		WriteTimeout:    10 * time.Second,
	}
}

type Server struct {
	signer *auth.Signer
	log    *slog.Logger
	opts   Options

	mu     sync.Mutex
	conns  map[*conn]struct{}
	closed bool
	wg     sync.WaitGroup // one per Serve call that is past the closed check
}

func NewServer(signer *auth.Signer, log *slog.Logger, opts Options) *Server {
	return &Server{signer: signer, log: log, opts: opts, conns: make(map[*conn]struct{})}
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
	err := c.readLoop(s.handleMessage)
	c.fail(websocket.StatusNormalClosure, "")
	wg.Wait()

	code, reason := c.closeReason()
	log.Info("client disconnected", "code", code, "reason", reason, "read_err", err)
}

// handleMessage is the milestone 0 echo handler: it answers ping with pong
// and echoes everything else. Milestone 1 replaces it with routing to the
// session actor.
func (s *Server) handleMessage(c *conn, msg []byte) {
	var env struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &env) == nil && env.Type == "ping" {
		c.enqueue(pongMsg)
		return
	}
	c.enqueue(msg)
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
