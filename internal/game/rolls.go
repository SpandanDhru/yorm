package game

import (
	"errors"
	"fmt"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/dice"
)

// MaxRolls is how many recent rolls the state keeps for the log. Older
// ones stay in the event history.
const MaxRolls = 50

// DiceRolled is a roll for the shared log: rolled by the server, or
// entered from physical dice.
type DiceRolled struct {
	Expr     string      `json:"expr"` // canonical, e.g. "2d20kh1+5"
	Label    string      `json:"label,omitempty"`
	Result   dice.Result `json:"result"` // no terms when only a physical total was entered
	Physical bool        `json:"physical"`
}

func (DiceRolled) EventName() string { return "DiceRolled" }

// Roll is a DiceRolled with who and when, as kept in State.Rolls.
type Roll struct {
	Seq int64     `json:"seq"`
	By  UserID    `json:"by"`
	At  time.Time `json:"at"`
	DiceRolled
}

const CodeInvalidExpression = "invalid_expression"

type rollDiceArgs struct {
	Text string `json:"text"` // "1d20+5 longsword", "2d6+3 = 4 5", "1d20+5 = 17"
}

func decideRollDice(s *State, cmd Command, env Env) ([]Payload, error) {
	if s.Members[cmd.By].Role == auth.RoleSpectator {
		return nil, reject(CodeForbidden, "spectators can't roll")
	}
	var a rollDiceArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	l, err := dice.ParseLine(a.Text)
	if err != nil {
		if errors.Is(err, dice.ErrSyntax) {
			return nil, reject(CodeInvalidExpression, `try something like "1d20+5" or "2d6+3 = 4 5"`)
		}
		return nil, reject(CodeInvalidExpression, err.Error())
	}
	ev := DiceRolled{Expr: l.Expr.String(), Label: l.Label}
	switch {
	case l.Faces != nil:
		if ev.Result, err = l.Expr.WithFaces(l.Faces); err != nil {
			return nil, reject(CodeInvalidExpression, err.Error())
		}
		ev.Physical = true
	case l.Total != nil:
		if lo, hi := l.Expr.Bounds(); *l.Total < lo || *l.Total > hi {
			return nil, reject(CodeInvalidExpression, fmt.Sprintf("%s can only total %d to %d", ev.Expr, lo, hi))
		}
		ev.Result, ev.Physical = dice.Result{Total: *l.Total}, true
	default:
		ev.Result = l.Expr.Roll(env.Roll)
	}
	return []Payload{ev}, nil
}
