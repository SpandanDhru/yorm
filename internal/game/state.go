// Package game holds the rules and the state of one session. It does no I/O:
// Decide validates a command against the state and returns the events it
// produces, and Apply folds events into the state. Apply is deterministic,
// so replaying a session's events always rebuilds the same state.
package game

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/SpandanDhru/yorm/internal/auth"
)

type (
	TokenID string
	UserID  string
	ActorID string
)

// Cell is a grid position; {0, 0} is the top-left cell.
type Cell struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// State is everything the session actor knows about one session. Only the
// actor goroutine touches it.
type State struct {
	ID        string                 `json:"id"`
	Seq       int64                  `json:"seq"` // last applied event
	Settings  Settings               `json:"settings"`
	Map       *Map                   `json:"map"` // nil until the DM sets one
	Tokens    map[TokenID]*Token     `json:"tokens"`
	Actors    map[ActorID]*Character `json:"actors"` // PCs, NPCs, and monsters
	Members   map[UserID]*Member     `json:"members"`
	Encounter *Encounter             `json:"encounter"` // nil outside combat
}

// Settings are the session's house rules.
type Settings struct {
	Diagonal DiagonalRule `json:"diagonal"`
}

type DiagonalRule string

const (
	DiagonalFive        DiagonalRule = "5"      // every diagonal step costs one cell
	DiagonalAlternating DiagonalRule = "5-10-5" // every second diagonal step costs two
)

type Map struct {
	ID         string  `json:"id"`
	ImageURL   string  `json:"image_url"`  // empty for a blank map
	Background string  `json:"background"` // #rrggbb, shown when there is no image
	Cols       int     `json:"cols"`
	Rows       int     `json:"rows"`
	CellFeet   int     `json:"cell_feet"`
	Terrain    Terrain `json:"terrain"`
}

// Terrain holds the painted cells; unpainted cells are clear. In JSON it
// is an object keyed by "x,y".
type Terrain map[Cell]TerrainKind

func (t Terrain) MarshalJSON() ([]byte, error) {
	m := make(map[string]TerrainKind, len(t))
	for c, k := range t {
		m[fmt.Sprintf("%d,%d", c.X, c.Y)] = k
	}
	return json.Marshal(m)
}

func (t *Terrain) UnmarshalJSON(b []byte) error {
	var m map[string]TerrainKind
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*t = make(Terrain, len(m))
	for key, k := range m {
		var c Cell
		if _, err := fmt.Sscanf(key, "%d,%d", &c.X, &c.Y); err != nil {
			return fmt.Errorf("game: bad terrain cell %q", key)
		}
		(*t)[c] = k
	}
	return nil
}

type TerrainKind string

const (
	TerrainWall      TerrainKind = "wall"      // cannot be entered
	TerrainDifficult TerrainKind = "difficult" // costs double
	TerrainWater     TerrainKind = "water"     // costs double (swimming)
	TerrainHazard    TerrainKind = "hazard"    // a marker for the DM; no rule
)

func (k TerrainKind) valid() bool {
	switch k {
	case TerrainWall, TerrainDifficult, TerrainWater, TerrainHazard:
		return true
	}
	return false
}

// InBounds reports whether a token of the given size placed at c fits on the map.
func (m *Map) InBounds(c Cell, size int) bool {
	return c.X >= 0 && c.Y >= 0 && c.X+size <= m.Cols && c.Y+size <= m.Rows
}

type Token struct {
	ID          TokenID  `json:"id"`
	Actor       ActorID  `json:"actor,omitempty"` // the character this token stands for, if any
	Label       string   `json:"label"`
	Color       string   `json:"color"` // #rrggbb
	Pos         Cell     `json:"pos"`
	Size        int      `json:"size"` // cells per side: 1 medium, 2 large
	Controllers []UserID `json:"controllers"`
}

type ActorKind string

const (
	KindPC      ActorKind = "pc"
	KindNPC     ActorKind = "npc"
	KindMonster ActorKind = "monster"
)

// Character is a character card: the stats the table tracks, not a full
// character sheet.
type Character struct {
	ID           ActorID     `json:"id"`
	Kind         ActorKind   `json:"kind"`
	Name         string      `json:"name"`
	Class        string      `json:"class"` // or creature type for monsters
	Level        int         `json:"level"`
	AC           int         `json:"ac"`
	Speed        int         `json:"speed"` // feet per turn
	InitBonus    int         `json:"init_bonus"`
	HP           HitPoints   `json:"hp"`
	Conditions   []Condition `json:"conditions"`
	Controllers  []UserID    `json:"controllers"`
	RollsOwnDice bool        `json:"rolls_own_dice"` // enters physical rolls instead of server rolls
}

type HitPoints struct {
	Current int `json:"current"`
	Max     int `json:"max"`
	Temp    int `json:"temp"`
}

// Damage applies n points of damage: temporary HP absorbs it first, and
// HP never drops below 0.
func (hp HitPoints) Damage(n int) HitPoints {
	absorbed := min(hp.Temp, n)
	hp.Temp -= absorbed
	hp.Current = max(0, hp.Current-(n-absorbed))
	return hp
}

// Heal restores n points, up to max.
func (hp HitPoints) Heal(n int) HitPoints {
	hp.Current = min(hp.Max, hp.Current+n)
	return hp
}

type Condition struct {
	Name   string `json:"name"`             // "Prone", "Poisoned", homebrew allowed
	Source string `json:"source,omitempty"` // who or what caused it
}

type Member struct {
	UserID      UserID    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        auth.Role `json:"role"`
}

func NewState(id string) *State {
	return &State{
		ID:       id,
		Settings: Settings{Diagonal: DiagonalFive},
		Tokens:   map[TokenID]*Token{},
		Actors:   map[ActorID]*Character{},
		Members:  map[UserID]*Member{},
	}
}

// Apply folds ev into s. It must stay deterministic: no clocks, randomness,
// or I/O, since replay depends on it. Events were validated when they were
// decided, so Apply trusts them.
func (s *State) Apply(ev Event) {
	s.Seq = ev.Seq
	s.applyCombat(ev.Data)
	switch d := ev.Data.(type) {
	case MemberJoined:
		m := d.Member
		s.Members[m.UserID] = &m
	case MapSet:
		m := d.Map
		m.Terrain = maps.Clone(m.Terrain) // painting mutates it; the event must not change
		if m.Terrain == nil {
			m.Terrain = Terrain{}
		}
		s.Map = &m
	case CellsPainted:
		if s.Map == nil {
			break
		}
		for _, c := range d.cells() {
			if d.Terrain == TerrainClear {
				delete(s.Map.Terrain, c)
			} else {
				s.Map.Terrain[c] = d.Terrain
			}
		}
	case SettingsChanged:
		s.Settings = d.Settings
	case TokenPlaced:
		t := d.Token
		s.Tokens[t.ID] = &t
	case TokenMoved:
		if t := s.Tokens[d.Token]; t != nil {
			t.Pos = d.To
		}
	case TokenRemoved:
		delete(s.Tokens, d.Token)
	case CharacterCreated:
		s.Actors[d.Character.ID] = cloneCharacter(d.Character)
	case CharacterUpdated:
		s.Actors[d.Character.ID] = cloneCharacter(d.Character)
	case CharacterDeleted:
		delete(s.Actors, d.Actor)
	case HPChanged:
		if a := s.Actors[d.Actor]; a != nil {
			a.HP = d.HP
		}
	case ConditionAdded:
		if a := s.Actors[d.Actor]; a != nil {
			a.Conditions = append(slices.Clone(a.Conditions), d.Condition)
		}
	case ConditionRemoved:
		if a := s.Actors[d.Actor]; a != nil {
			a.Conditions = slices.DeleteFunc(slices.Clone(a.Conditions), func(c Condition) bool { return c.Name == d.Name })
		}
	}
}

// cloneCharacter copies c so state never shares slices with an event.
func cloneCharacter(c Character) *Character {
	c.Conditions = slices.Clone(c.Conditions)
	c.Controllers = slices.Clone(c.Controllers)
	return &c
}

// CanControl reports whether user may move token t: the DM, the token's
// own controllers, or its character's.
func (s *State) CanControl(user UserID, t *Token) bool {
	if s.IsDM(user) || slices.Contains(t.Controllers, user) {
		return true
	}
	a := s.Actors[t.Actor]
	return a != nil && slices.Contains(a.Controllers, user)
}

// CanEdit reports whether user may change character a's card.
func (s *State) CanEdit(user UserID, a *Character) bool {
	return s.IsDM(user) || slices.Contains(a.Controllers, user)
}

// TokenFor returns the token standing for actor, or nil.
func (s *State) TokenFor(actor ActorID) *Token {
	for _, t := range s.Tokens {
		if t.Actor == actor {
			return t
		}
	}
	return nil
}

func (s *State) IsDM(user UserID) bool {
	m := s.Members[user]
	return m != nil && m.Role == auth.RoleDM
}
