package game

import (
	"encoding/json"
	"fmt"
	"slices"
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

// CellsPainted sets the terrain of Cells, plus every cell of Rect if set.
// A rectangle stays one small event however large it is.
type CellsPainted struct {
	Terrain TerrainKind `json:"terrain"` // or TerrainClear
	Cells   []Cell      `json:"cells,omitempty"`
	Rect    *Rect       `json:"rect,omitempty"`
}

// TerrainClear in CellsPainted erases the cells.
const TerrainClear TerrainKind = "clear"

// Rect is an inclusive cell rectangle; From and To may be any two corners.
type Rect struct {
	From Cell `json:"from"`
	To   Cell `json:"to"`
}

func (e CellsPainted) cells() []Cell {
	cells := slices.Clone(e.Cells)
	if r := e.Rect; r != nil {
		for y := min(r.From.Y, r.To.Y); y <= max(r.From.Y, r.To.Y); y++ {
			for x := min(r.From.X, r.To.X); x <= max(r.From.X, r.To.X); x++ {
				cells = append(cells, Cell{x, y})
			}
		}
	}
	return cells
}

type SettingsChanged struct {
	Settings Settings `json:"settings"`
}

type CharacterCreated struct {
	Character Character `json:"character"`
}

// CharacterUpdated replaces the card's stats. HP and conditions change
// through their own events.
type CharacterUpdated struct {
	Character Character `json:"character"`
}

type CharacterDeleted struct {
	Actor ActorID `json:"actor"`
}

type HPChanged struct {
	Actor ActorID   `json:"actor"`
	HP    HitPoints `json:"hp"`    // after the change
	Delta int       `json:"delta"` // what was asked: negative for damage; 0 for a temp HP change
}

type ConditionAdded struct {
	Actor     ActorID   `json:"actor"`
	Condition Condition `json:"condition"`
}

type ConditionRemoved struct {
	Actor ActorID `json:"actor"`
	Name  string  `json:"name"`
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

func (MemberJoined) EventName() string     { return "MemberJoined" }
func (MapSet) EventName() string           { return "MapSet" }
func (CellsPainted) EventName() string     { return "CellsPainted" }
func (SettingsChanged) EventName() string  { return "SettingsChanged" }
func (TokenPlaced) EventName() string      { return "TokenPlaced" }
func (TokenMoved) EventName() string       { return "TokenMoved" }
func (TokenRemoved) EventName() string     { return "TokenRemoved" }
func (CharacterCreated) EventName() string { return "CharacterCreated" }
func (CharacterUpdated) EventName() string { return "CharacterUpdated" }
func (CharacterDeleted) EventName() string { return "CharacterDeleted" }
func (HPChanged) EventName() string        { return "HPChanged" }
func (ConditionAdded) EventName() string   { return "ConditionAdded" }
func (ConditionRemoved) EventName() string { return "ConditionRemoved" }

var payloads = map[string]func([]byte) (Payload, error){
	"MemberJoined":     decode[MemberJoined],
	"MapSet":           decode[MapSet],
	"CellsPainted":     decode[CellsPainted],
	"SettingsChanged":  decode[SettingsChanged],
	"TokenPlaced":      decode[TokenPlaced],
	"TokenMoved":       decode[TokenMoved],
	"TokenRemoved":     decode[TokenRemoved],
	"CharacterCreated": decode[CharacterCreated],
	"CharacterUpdated": decode[CharacterUpdated],
	"CharacterDeleted": decode[CharacterDeleted],
	"HPChanged":        decode[HPChanged],
	"ConditionAdded":   decode[ConditionAdded],
	"ConditionRemoved": decode[ConditionRemoved],
	"CombatStarted":    decode[CombatStarted],
	"InitiativeSet":    decode[InitiativeSet],
	"CombatantRemoved": decode[CombatantRemoved],
	"TurnStarted":      decode[TurnStarted],
	"TurnEnded":        decode[TurnEnded],
	"MovementSpent":    decode[MovementSpent],
	"ActionUsed":       decode[ActionUsed],
	"CombatEnded":      decode[CombatEnded],
	"DiceRolled":       decode[DiceRolled],
}

func decode[T Payload](data []byte) (Payload, error) {
	var p T
	err := json.Unmarshal(data, &p)
	return p, err
}

// versions holds each event's current data version; unlisted events are
// at version 1. When an event's data changes shape, bump its version here
// and add an upcaster from the old version, so stored sessions still
// replay.
var versions = map[string]int{}

// upcasters[name][v] converts an event's data from version v to v+1.
var upcasters = map[string]map[int]func(json.RawMessage) (json.RawMessage, error){}

// Version returns the version new events called name are stored with.
func Version(name string) int {
	if v, ok := versions[name]; ok {
		return v
	}
	return 1
}

// DecodePayload decodes the stored data of an event called name, stored at
// the given version, upcasting it to the current version first.
func DecodePayload(name string, version int, data []byte) (Payload, error) {
	dec, ok := payloads[name]
	if !ok {
		return nil, fmt.Errorf("game: unknown event %q", name)
	}
	for v := version; v < Version(name); v++ {
		up := upcasters[name][v]
		if up == nil {
			return nil, fmt.Errorf("game: no upcaster for %s from version %d", name, v)
		}
		var err error
		if data, err = up(data); err != nil {
			return nil, fmt.Errorf("game: upcast %s from version %d: %w", name, v, err)
		}
	}
	if version > Version(name) {
		return nil, fmt.Errorf("game: %s version %d is newer than this server knows", name, version)
	}
	p, err := dec(data)
	if err != nil {
		return nil, fmt.Errorf("game: decode %s: %w", name, err)
	}
	return p, nil
}
