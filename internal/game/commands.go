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
}

type decider func(s *State, cmd Command, env Env) ([]Payload, error)

var deciders = map[string]decider{
	"set_map":      decideSetMap,
	"place_token":  decidePlaceToken,
	"move_token":   decideMoveToken,
	"remove_token": decideRemoveToken,
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
	ImageURL string `json:"image_url"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	CellFeet int    `json:"cell_feet"`
}

func decideSetMap(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can set the map")
	}
	var a setMapArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	name, ok := strings.CutPrefix(a.ImageURL, UploadsPrefix)
	if !ok || name == "" || strings.ContainsAny(name, "/\\") {
		return nil, reject(CodeInvalidTarget, "image_url must be an uploaded image")
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
	id := env.NewID("map")
	if s.Map != nil && s.Map.ImageURL == a.ImageURL {
		id = s.Map.ID // same image, new grid settings
	}
	return []Payload{MapSet{Map: Map{ID: id, ImageURL: a.ImageURL, Cols: a.Cols, Rows: a.Rows, CellFeet: a.CellFeet}}}, nil
}

type placeTokenArgs struct {
	Label       string   `json:"label"`
	Color       string   `json:"color"`
	At          Cell     `json:"at"`
	Size        int      `json:"size"`
	Controllers []UserID `json:"controllers"`
}

var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

const defaultColor = "#8a8f98"

func decidePlaceToken(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can place tokens")
	}
	var a placeTokenArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
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
		ID: TokenID(env.NewID("tok")), Label: a.Label, Color: a.Color,
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
	return []Payload{TokenRemoved{Token: a.Token}}, nil //nolint:staticcheck // args and events evolve separately
}
