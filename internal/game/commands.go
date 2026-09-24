package game

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Command is a request from a member. It can be rejected.
type Command struct {
	ID   string          `json:"id"` // client-generated, echoed in ack and reject
	By   UserID          `json:"-"`  // set by the server from the sender's token, never from the client
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Reject codes sent to clients.
const (
	CodeForbidden      = "forbidden"
	CodeInvalidTarget  = "invalid_target"
	CodeInvalidCommand = "invalid_command"
	CodeConflict       = "conflict"
	CodeNotYourTurn    = "not_your_turn"
	CodeOutOfMovement  = "out_of_movement"
	CodeUnavailable    = "unavailable"
)

// Reject is why a command was refused.
type Reject struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (r *Reject) Error() string { return r.Code + ": " + r.Message }

func reject(code, msg string) *Reject { return &Reject{Code: code, Message: msg} }

// Env supplies what Decide may not produce itself. Randomness lives here,
// outside Apply, so generated values end up in events and replay exactly.
type Env struct {
	NewID func(prefix string) string
	// Roll returns a uniformly random integer from 1 to sides.
	Roll func(sides int) int
}

type decider func(s *State, cmd Command, env Env) ([]Payload, error)

var deciders = map[string]decider{
	"set_map":      decideSetMap,
	"paint_cells":  decidePaintCells,
	"set_settings": decideSetSettings,
	"place_token":  decidePlaceToken,
	"move_token":   decideMoveToken,
	"remove_token": decideRemoveToken,

	"create_character": decideCreateCharacter,
	"update_character": decideUpdateCharacter,
	"delete_character": decideDeleteCharacter,
	"adjust_hp":        decideAdjustHP,
	"set_temp_hp":      decideSetTempHP,
	"add_condition":    decideAddCondition,
	"remove_condition": decideRemoveCondition,

	"start_combat":       decideStartCombat,
	"set_initiative":     decideSetInitiative,
	"begin_combat":       decideBeginCombat,
	"join_combat":        decideJoinCombat,
	"remove_from_combat": decideRemoveFromCombat,
	"end_turn":           decideEndTurn,
	"prev_turn":          decidePrevTurn,
	"use_action":         decideUseAction,
	"end_combat":         decideEndCombat,
}

// Decide validates cmd against s and returns the events it produces. The
// error is a *Reject when the command is refused.
func Decide(s *State, cmd Command, env Env) ([]Payload, error) {
	d, ok := deciders[cmd.Name]
	if !ok {
		return nil, reject(CodeInvalidCommand, "unknown command "+cmd.Name)
	}
	if s.Members[cmd.By] == nil {
		return nil, reject(CodeForbidden, "not a member of this session")
	}
	return d(s, cmd, env)
}

func decodeArgs(cmd Command, v any) error {
	if len(cmd.Args) == 0 {
		return reject(CodeInvalidCommand, "missing args")
	}
	if err := json.Unmarshal(cmd.Args, v); err != nil {
		return reject(CodeInvalidCommand, "bad args: "+err.Error())
	}
	return nil
}

const (
	maxGridCells = 200
	maxTokenSize = 4
	maxLabelLen  = 32
)

// UploadsPrefix is where map images are served. set_map only accepts images
// from here, so a DM cannot point players' browsers at another site.
const UploadsPrefix = "/uploads/"

type setMapArgs struct {
	ImageURL   string `json:"image_url"`  // empty for a blank map
	Background string `json:"background"` // blank maps; default parchment
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	CellFeet   int    `json:"cell_feet"`
	// KeepTerrain changes the grid of the current map, keeping its ID and
	// the painted cells that still fit. Otherwise this is a new map.
	KeepTerrain bool `json:"keep_terrain"`
}

const defaultBackground = "#e8e0cc"

func decideSetMap(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can set the map")
	}
	var a setMapArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if a.ImageURL != "" {
		name, ok := strings.CutPrefix(a.ImageURL, UploadsPrefix)
		if !ok || name == "" || strings.ContainsAny(name, "/\\") {
			return nil, reject(CodeInvalidTarget, "image_url must be an uploaded image")
		}
	}
	if a.Background == "" {
		a.Background = defaultBackground
	}
	if !colorRE.MatchString(a.Background) {
		return nil, reject(CodeInvalidTarget, "background must look like #rrggbb")
	}
	if a.Cols < 1 || a.Cols > maxGridCells || a.Rows < 1 || a.Rows > maxGridCells {
		return nil, reject(CodeInvalidTarget, "cols and rows must be between 1 and 200")
	}
	if a.CellFeet == 0 {
		a.CellFeet = 5
	}
	if a.CellFeet < 1 || a.CellFeet > 100 {
		return nil, reject(CodeInvalidTarget, "cell_feet must be between 1 and 100")
	}
	m := Map{
		ID: env.NewID("map"), ImageURL: a.ImageURL, Background: a.Background,
		Cols: a.Cols, Rows: a.Rows, CellFeet: a.CellFeet, Terrain: Terrain{},
	}
	if a.KeepTerrain {
		if s.Map == nil {
			return nil, reject(CodeInvalidTarget, "there is no map to adjust")
		}
		m.ID = s.Map.ID
		for c, k := range s.Map.Terrain {
			if m.InBounds(c, 1) {
				m.Terrain[c] = k
			}
		}
	}
	return []Payload{MapSet{Map: m}}, nil
}

// maxPaintCells bounds one paint_cells command; a brush stroke is split
// into several commands, and big areas use a rectangle.
const maxPaintCells = 2000

type paintCellsArgs struct {
	Terrain TerrainKind `json:"terrain"`
	Cells   []Cell      `json:"cells"`
	Rect    *Rect       `json:"rect"`
}

func decidePaintCells(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can paint terrain")
	}
	var a paintCellsArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if a.Terrain != TerrainClear && !a.Terrain.valid() {
		return nil, reject(CodeInvalidTarget, "unknown terrain "+string(a.Terrain))
	}
	if s.Map == nil {
		return nil, reject(CodeInvalidTarget, "set a map first")
	}
	if len(a.Cells) == 0 && a.Rect == nil {
		return nil, reject(CodeInvalidTarget, "nothing to paint")
	}
	if len(a.Cells) > maxPaintCells {
		return nil, reject(CodeInvalidTarget, "too many cells in one stroke")
	}
	for _, c := range a.Cells {
		if !s.Map.InBounds(c, 1) {
			return nil, reject(CodeInvalidTarget, "cell is off the map")
		}
	}
	if r := a.Rect; r != nil && (!s.Map.InBounds(r.From, 1) || !s.Map.InBounds(r.To, 1)) {
		return nil, reject(CodeInvalidTarget, "rectangle is off the map")
	}
	return []Payload{CellsPainted{Terrain: a.Terrain, Cells: a.Cells, Rect: a.Rect}}, nil
}

func decideSetSettings(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can change settings")
	}
	var a Settings
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if a.Diagonal != DiagonalFive && a.Diagonal != DiagonalAlternating {
		return nil, reject(CodeInvalidTarget, `diagonal must be "5" or "5-10-5"`)
	}
	return []Payload{SettingsChanged{Settings: a}}, nil
}

type placeTokenArgs struct {
	Actor       ActorID  `json:"actor"` // optional: the character the token stands for
	Label       string   `json:"label"`
	Color       string   `json:"color"`
	At          Cell     `json:"at"`
	Size        int      `json:"size"`
	Controllers []UserID `json:"controllers"`
}

var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

const defaultColor = "#8a8f98"

// kindColors are the default token colors for character tokens.
var kindColors = map[ActorKind]string{KindPC: "#2f6fd1", KindNPC: "#8a8f98", KindMonster: "#c0392b"}

func decidePlaceToken(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can place tokens")
	}
	var a placeTokenArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if a.Actor != "" {
		c := s.Actors[a.Actor]
		if c == nil {
			return nil, reject(CodeInvalidTarget, "no such character")
		}
		if s.TokenFor(a.Actor) != nil {
			return nil, reject(CodeInvalidTarget, c.Name+" already has a token")
		}
		if len(a.Controllers) > 0 {
			return nil, reject(CodeInvalidTarget, "a character's token is controlled by the character's controllers")
		}
		if a.Label == "" {
			a.Label = c.Name
		}
		if a.Color == "" {
			a.Color = kindColors[c.Kind]
		}
	}
	a.Label = strings.TrimSpace(a.Label)
	if a.Label == "" || utf8.RuneCountInString(a.Label) > maxLabelLen {
		return nil, reject(CodeInvalidTarget, "label must be 1 to 32 characters")
	}
	if a.Color == "" {
		a.Color = defaultColor
	}
	if !colorRE.MatchString(a.Color) {
		return nil, reject(CodeInvalidTarget, "color must look like #rrggbb")
	}
	if a.Size == 0 {
		a.Size = 1
	}
	if a.Size < 1 || a.Size > maxTokenSize {
		return nil, reject(CodeInvalidTarget, "size must be between 1 and 4")
	}
	if s.Map == nil {
		return nil, reject(CodeInvalidTarget, "upload a map first")
	}
	if !s.Map.InBounds(a.At, a.Size) {
		return nil, reject(CodeInvalidTarget, "token does not fit there")
	}
	controllers := []UserID{}
	for _, u := range a.Controllers {
		if s.Members[u] == nil {
			return nil, reject(CodeInvalidTarget, "controller "+string(u)+" is not a member")
		}
		if !slices.Contains(controllers, u) {
			controllers = append(controllers, u)
		}
	}
	t := Token{
		ID: TokenID(env.NewID("tok")), Actor: a.Actor, Label: a.Label, Color: a.Color,
		Pos: a.At, Size: a.Size, Controllers: controllers,
	}
	return []Payload{TokenPlaced{Token: t}}, nil
}

type moveTokenArgs struct {
	Token TokenID `json:"token"`
	To    Cell    `json:"to"`
}

func decideMoveToken(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a moveTokenArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	t := s.Tokens[a.Token]
	if t == nil {
		return nil, reject(CodeInvalidTarget, "no such token")
	}
	if !s.CanControl(cmd.By, t) {
		return nil, reject(CodeForbidden, "you do not control this token")
	}
	if s.Map == nil || !s.Map.InBounds(a.To, t.Size) {
		return nil, reject(CodeInvalidTarget, "target is off the map")
	}
	if evs, err := combatMove(s, cmd.By, t, a.To); evs != nil || err != nil {
		return evs, err
	}
	// The DM can put a token anywhere; everyone else has to walk.
	if !s.IsDM(cmd.By) {
		if _, ok := PathCost(s.Map, s.Settings.Diagonal, t.Size, t.Pos, a.To, -1); !ok {
			return nil, reject(CodeInvalidTarget, "no way through: walls are in the way")
		}
	}
	return []Payload{TokenMoved{Token: t.ID, From: t.Pos, To: a.To}}, nil
}

type removeTokenArgs struct {
	Token TokenID `json:"token"`
}

func decideRemoveToken(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can remove tokens")
	}
	var a removeTokenArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if s.Tokens[a.Token] == nil {
		return nil, reject(CodeInvalidTarget, "no such token")
	}
	return []Payload{TokenRemoved{Token: a.Token}}, nil
}
