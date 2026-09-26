package session

import (
	"encoding/json"

	"github.com/SpandanDhru/yorm/internal/game"
)

// Server-to-client messages the actor sends. The connection layer sends
// welcome and pong itself.

type eventMsg struct {
	Type string `json:"type"` // "event"
	game.Event
}

// eventsMsg is a batch of missed events, sent in answer to a sync. One
// message rather than one per event, so catching up can't overflow the
// client's send buffer.
type eventsMsg struct {
	Type   string       `json:"type"` // "events"
	Seq    int64        `json:"seq"`  // the session's seq; the batch ends here
	Events []game.Event `json:"events"`
}

type snapshotMsg struct {
	Type  string      `json:"type"` // "snapshot"
	Seq   int64       `json:"seq"`
	State *game.State `json:"state"`
}

type ackMsg struct {
	Type string `json:"type"` // "ack"
	ID   string `json:"id"`
	Seq  int64  `json:"seq"` // last seq after the command; its events were sent before the ack
}

type rejectMsg struct {
	Type string `json:"type"` // "reject"
	ID   string `json:"id"`
	game.Reject
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // unreachable: every message is built from plain structs, maps, and strings
	}
	return b
}
