package game

type setTokenHiddenArgs struct {
	Token  TokenID `json:"token"`
	Hidden bool    `json:"hidden"`
}

func decideSetTokenHidden(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can hide tokens")
	}
	var a setTokenHiddenArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	t := s.Tokens[a.Token]
	if t == nil {
		return nil, reject(CodeInvalidTarget, "no such token")
	}
	if t.Hidden == a.Hidden {
		return nil, reject(CodeInvalidTarget, map[bool]string{true: "already hidden", false: "already visible"}[a.Hidden])
	}
	if a.Hidden {
		return []Payload{TokenHidden{Token: t.ID}}, nil
	}
	return []Payload{TokenRevealed{Token: t.ID}}, nil
}

type setFogArgs struct {
	Enabled bool `json:"enabled"`
}

func decideSetFog(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM controls the fog")
	}
	var a setFogArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	if s.Map == nil {
		return nil, reject(CodeInvalidTarget, "set a map first")
	}
	if s.Map.Fog.Enabled == a.Enabled {
		return nil, reject(CodeInvalidTarget, map[bool]string{true: "fog is already on", false: "fog is already off"}[a.Enabled])
	}
	return []Payload{FogSet{Enabled: a.Enabled}}, nil
}

type paintFogArgs struct {
	For   UserID `json:"for"` // a player; empty for the whole party
	Cells []Cell `json:"cells"`
	Rect  *Rect  `json:"rect"`
}

// decidePaintFog reveals cells (or covers them again) for the party or one
// player, like painting terrain.
func decidePaintFog(reveal bool) decider {
	return func(s *State, cmd Command, _ Env) ([]Payload, error) {
		if !s.IsDM(cmd.By) {
			return nil, reject(CodeForbidden, "only the DM controls the fog")
		}
		var a paintFogArgs
		if err := decodeArgs(cmd, &a); err != nil {
			return nil, err
		}
		if s.Map == nil {
			return nil, reject(CodeInvalidTarget, "set a map first")
		}
		if a.For != "" && (s.Members[a.For] == nil || s.IsDM(a.For)) {
			return nil, reject(CodeInvalidTarget, "fog can be revealed for the party or one player")
		}
		if len(a.Cells) == 0 && a.Rect == nil {
			return nil, reject(CodeInvalidTarget, "nothing to reveal")
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
		if reveal {
			return []Payload{FogRevealed{For: a.For, Cells: a.Cells, Rect: a.Rect}}, nil
		}
		return []Payload{FogHidden{For: a.For, Cells: a.Cells, Rect: a.Rect}}, nil
	}
}
