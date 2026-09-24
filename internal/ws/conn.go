package ws

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/SpandanDhru/yorm/internal/auth"
)

// conn is one connected client. The server's handler goroutine reads, a
// writer goroutine drains send, and a pinger checks liveness.
//
// Only writeLoop closes the socket, so the close code sent to the client is
// always the one given to fail. Reads never use a cancellable context,
// because cancelling one makes the library drop the socket without sending
// a close frame.
type conn struct {
	ws     *websocket.Conn
	claims auth.Claims
	send   chan []byte

	ctx    context.Context // done once the connection should close
	cancel context.CancelFunc
	once   sync.Once
	code   websocket.StatusCode
	reason string
	abort  bool // skip the close handshake; the client is not responding
}

func newConn(ws *websocket.Conn, claims auth.Claims, sendBuffer int) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &conn{ws: ws, claims: claims, send: make(chan []byte, sendBuffer), ctx: ctx, cancel: cancel}
}

// enqueue queues msg for the writer and never blocks. When the buffer is
// full the client is too slow to keep up, so it is disconnected; it resyncs
// when it reconnects, and nobody else at the table waits for it.
func (c *conn) enqueue(msg []byte) bool {
	select {
	case c.send <- msg:
		return true
	case <-c.ctx.Done():
		return false
	default:
		c.fail(websocket.StatusTryAgainLater, "send buffer full")
		return false
	}
}

// fail starts closing the connection. Only the first call to fail or
// abortDead has any effect.
func (c *conn) fail(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		c.code, c.reason = code, reason
		c.cancel()
	})
}

// abortDead closes without the close handshake, which would otherwise wait
// up to 10 seconds for a reply from a client that has stopped responding.
func (c *conn) abortDead(reason string) {
	c.once.Do(func() {
		c.code, c.reason, c.abort = websocket.StatusPolicyViolation, reason, true
		c.cancel()
	})
}

// closeReason returns what fail recorded. Call it only after the connection is done.
func (c *conn) closeReason() (websocket.StatusCode, string) {
	<-c.ctx.Done()
	return c.code, c.reason
}

func (c *conn) writeLoop(timeout time.Duration) {
	for {
		select {
		case <-c.ctx.Done():
			// Closing unblocks readLoop and pingLoop.
			if c.abort {
				_ = c.ws.CloseNow()
			} else {
				_ = c.ws.Close(c.code, c.reason)
			}
			return
		case msg := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := c.ws.Write(ctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				c.fail(websocket.StatusInternalError, "write failed")
			}
		}
	}
}

// readLoop passes each text message to handle until the connection fails.
func (c *conn) readLoop(handle func(*conn, []byte)) error {
	for {
		typ, msg, err := c.ws.Read(context.Background())
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			c.fail(websocket.StatusUnsupportedData, "text messages only")
			return nil
		}
		handle(c, msg)
	}
}

// pingLoop checks liveness with protocol-level pings, which browsers answer
// automatically. A client that sends no pong within maxMissed intervals is
// dropped.
func (c *conn) pingLoop(interval time.Duration, maxMissed int) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(maxMissed)*interval)
		err := c.ws.Ping(ctx)
		cancel()
		if err != nil {
			c.abortDead("ping timeout")
			return
		}
	}
}
