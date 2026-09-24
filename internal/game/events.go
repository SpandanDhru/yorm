package game

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event is a fact about a session. Events are never deleted, only followed
// by compensating events.
type Event struct {
	Seq   int64     `json:"seq"`
	Name  string    `json:"name"`
	By    UserID    `json:"by"`
	Cause string    `json:"cause,omitempty"` // ID of the command that produced it
	At    time.Time `json:"at"`
	Data  Payload   `json:"data"`
}

// Payload is the event-specific part of an Event.
type Payload interface {
	EventName() string
}

type MemberJoined struct {
	Member Member `json:"member"`
}

type MapSet struct {
	Map Map `json:"map"`
}

type TokenPlaced struct {
	Token Token `json:"token"`
}

type TokenMoved struct {
	Token TokenID `json:"token"`
	From  Cell    `json:"from"`
	To    Cell    `json:"to"`
}

type TokenRemoved struct {
	Token TokenID `json:"token"`
}

func (MemberJoined) EventName() string { return "MemberJoined" }
func (MapSet) EventName() string       { return "MapSet" }
func (TokenPlaced) EventName() string  { return "TokenPlaced" }
func (TokenMoved) EventName() string   { return "TokenMoved" }
func (TokenRemoved) EventName() string { return "TokenRemoved" }

var payloads = map[string]func([]byte) (Payload, error){
	"MemberJoined": decode[MemberJoined],
	"MapSet":       decode[MapSet],
	"TokenPlaced":  decode[TokenPlaced],
	"TokenMoved":   decode[TokenMoved],
	"TokenRemoved": decode[TokenRemoved],
}

func decode[T Payload](data []byte) (Payload, error) {
	var p T
	err := json.Unmarshal(data, &p)
	return p, err
}

// DecodePayload decodes the stored data of an event called name.
func DecodePayload(name string, data []byte) (Payload, error) {
	dec, ok := payloads[name]
	if !ok {
		return nil, fmt.Errorf("game: unknown event %q", name)
	}
	p, err := dec(data)
	if err != nil {
		return nil, fmt.Errorf("game: decode %s: %w", name, err)
	}
	return p, nil
}
