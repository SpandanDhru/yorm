package game

import (
	"math"
	"slices"
	"strings"
)

type setTokenImageArgs struct {
	Token TokenID `json:"token"`
	Image string  `json:"image"` // empty to remove it
}

// decideSetTokenImage lets the DM, or whoever controls a token, give it a
// picture.
func decideSetTokenImage(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a setTokenImageArgs
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
	if a.Image != "" && !s.uploaded(a.Image) {
		return nil, reject(CodeInvalidTarget, "image must be an uploaded image")
	}
	return []Payload{TokenImageSet{Token: t.ID, Image: a.Image}}, nil
}

// Limits on one pen stroke. A long stroke is sent as several.
const (
	maxDrawPoints = 400
	minDrawWidth  = 0.02
	maxDrawWidth  = 2
)

type drawArgs struct {
	Color  string    `json:"color"`
	Width  float64   `json:"width"`
	Points []float64 `json:"points"`
}

func decideDraw(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can draw")
	}
	var a drawArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if s.Map == nil {
		return nil, reject(CodeInvalidTarget, "set a map first")
	}
	if !colorRE.MatchString(a.Color) {
		return nil, reject(CodeInvalidTarget, "color must look like #rrggbb")
	}
	if !(a.Width >= minDrawWidth && a.Width <= maxDrawWidth) { // also false for NaN
		return nil, reject(CodeInvalidTarget, "width must be between 0.02 and 2 cells")
	}
	n := len(a.Points)
	if n < 2 || n%2 != 0 || n/2 > maxDrawPoints {
		return nil, reject(CodeInvalidTarget, "a stroke is 1 to 400 points")
	}
	for i, v := range a.Points {
		limit := float64(s.Map.Cols)
		if i%2 == 1 {
			limit = float64(s.Map.Rows)
		}
		if math.IsNaN(v) || v < 0 || v > limit {
			return nil, reject(CodeInvalidTarget, "stroke goes off the map")
		}
	}
	d := Drawing{ID: env.NewID("drw"), Color: strings.ToLower(a.Color), Width: a.Width, Points: a.Points}
	return []Payload{DrawingAdded{Drawing: d}}, nil
}

type eraseDrawingArgs struct {
	ID string `json:"id"`
}

func decideEraseDrawing(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can erase drawings")
	}
	var a eraseDrawingArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if s.Map == nil || !slices.ContainsFunc(s.Map.Drawings, func(d Drawing) bool { return d.ID == a.ID }) {
		return nil, reject(CodeInvalidTarget, "no such drawing")
	}
	return []Payload{DrawingErased{ID: a.ID}}, nil
}

func decideClearDrawings(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can erase drawings")
	}
	if s.Map == nil || len(s.Map.Drawings) == 0 {
		return nil, reject(CodeInvalidTarget, "there's nothing drawn")
	}
	return []Payload{DrawingsCleared{}}, nil
}
