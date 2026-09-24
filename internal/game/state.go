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
)

// Cell is a grid position; {0, 0} is the top-left cell.
type Cell struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// State is everything the session actor knows about one session. Only the
// actor goroutine touches it.
type State struct {
	ID       string             `json:"id"`
	Seq      int64              `json:"seq"` // last applied event
	Settings Settings           `json:"settings"`
	Map      *Map               `json:"map"` // nil until the DM sets one
	Tokens   map[TokenID]*Token `json:"tokens"`
	Members  map[UserID]*Member `json:"members"`
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
	Label       string   `json:"label"`
	Color       string   `json:"color"` // #rrggbb
	Pos         Cell     `json:"pos"`
	Size        int      `json:"size"` // cells per side: 1 medium, 2 large
	Controllers []UserID `json:"controllers"`
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
		Members:  map[UserID]*Member{},
	}
}

// Apply folds ev into s. It must stay deterministic: no clocks, randomness,
// or I/O, since replay depends on it. Events were validated when they were
// decided, so Apply trusts them.
func (s *State) Apply(ev Event) {
	s.Seq = ev.Seq
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
	}
}

// CanControl reports whether user may move token t.
func (s *State) CanControl(user UserID, t *Token) bool {
	return s.IsDM(user) || slices.Contains(t.Controllers, user)
}

func (s *State) IsDM(user UserID) bool {
	m := s.Members[user]
	return m != nil && m.Role == auth.RoleDM
}
