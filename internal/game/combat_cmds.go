package game

import (
	"fmt"
	"slices"
)

type startCombatArgs struct {
	Actors []ActorID `json:"actors"` // default: every character with a token on the map
}

// decideStartCombat rolls initiative for everyone who doesn't roll their own
// dice. Turns begin at once if nobody has a physical roll to enter.
func decideStartCombat(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can start combat")
	}
	if s.Encounter != nil {
		return nil, reject(CodeInvalidTarget, "combat has already started")
	}
	var a startCombatArgs
	if len(cmd.Args) > 0 {
		if err := decodeArgs(cmd, &a); err != nil {
			return nil, err
		}
	}
	actors := a.Actors
	if actors == nil {
		for _, t := range s.Tokens {
			if t.Actor != "" && s.Actors[t.Actor] != nil {
				actors = append(actors, t.Actor)
			}
		}
		slices.Sort(actors) // map order is random; events must not be
	}
	if len(actors) == 0 {
		return nil, reject(CodeInvalidTarget, "put some characters on the map first")
	}
	var order []InitEntry
	for _, id := range actors {
		c := s.Actors[id]
		if c == nil {
			return nil, reject(CodeInvalidTarget, "no such character "+string(id))
		}
		if slices.ContainsFunc(order, func(e InitEntry) bool { return e.Actor == id }) {
			continue
		}
		e := InitEntry{Actor: id, Bonus: c.InitBonus, TieBreak: env.Roll(20)}
		if !c.RollsOwnDice {
			roll := env.Roll(20)
			total := roll + c.InitBonus
			e.Roll, e.Total = &roll, &total
		}
		order = append(order, e)
	}
	evs := []Payload{CombatStarted{Order: order}}
	return appendFirstTurn(s, evs), nil
}

// appendFirstTurn adds the first TurnStarted if evs leave combat with every
// initiative in but no turn begun.
func appendFirstTurn(s *State, evs []Payload) []Payload {
	next := preview(s, evs)
	e := next.Encounter
	if e == nil || e.Started() || len(e.waiting()) > 0 || len(e.Order) == 0 {
		return evs
	}
	first := e.Order[0].Actor
	return append(evs, TurnStarted{Actor: first, Round: 1, Movement: next.Actors[first].Speed})
}

// preview returns what the encounter would look like after evs, without
// touching s. Decide uses it to reason about the resulting turn order.
func preview(s *State, evs []Payload) *State {
	p := &State{Actors: s.Actors}
	if s.Encounter != nil {
		e := *s.Encounter
		e.Order = cloneEntries(e.Order)
		e.ReactionUsed = map[ActorID]bool{}
		p.Encounter = &e
	}
	for _, ev := range evs {
		p.applyCombat(ev)
	}
	return p
}

type setInitiativeArgs struct {
	Actor ActorID `json:"actor"`
	Roll  *int    `json:"roll"`  // the d20 face; the bonus is added
	Total *int    `json:"total"` // or the whole total
}

// decideSetInitiative enters a physical initiative roll, or lets the DM
// change anyone's initiative. Players can only enter their own, once.
func decideSetInitiative(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a setInitiativeArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	e := s.Encounter
	if e == nil {
		return nil, reject(CodeInvalidTarget, "there is no combat")
	}
	i := e.index(a.Actor)
	if i < 0 {
		return nil, reject(CodeInvalidTarget, "that character is not in this fight")
	}
	entry := e.Order[i]
	c := s.Actors[a.Actor]
	isDM := s.IsDM(cmd.By)
	if !isDM && (!s.CanEdit(cmd.By, c) || entry.Total != nil) {
		return nil, reject(CodeForbidden, "only the DM can change initiative once it is set")
	}
	ev := InitiativeSet{Actor: a.Actor, Bonus: c.InitBonus, TieBreak: entry.TieBreak, Physical: true}
	switch {
	case a.Roll != nil && a.Total == nil:
		if *a.Roll < 1 || *a.Roll > 20 {
			return nil, reject(CodeInvalidTarget, "a d20 shows 1 to 20")
		}
		ev.Roll, ev.Total = a.Roll, *a.Roll+c.InitBonus
	case a.Total != nil && a.Roll == nil:
		lo, hi := c.InitBonus+1, c.InitBonus+20 // what a d20 plus the bonus can show
		if isDM {
			lo, hi = -20, 60 // the DM can override freely
		}
		if *a.Total < lo || *a.Total > hi {
			return nil, reject(CodeInvalidTarget, fmt.Sprintf("with a %+d bonus, the total must be %d to %d", c.InitBonus, lo, hi))
		}
		ev.Total = *a.Total
	default:
		return nil, reject(CodeInvalidTarget, "give either the d20 roll or the total")
	}
	return appendFirstTurn(s, []Payload{ev}), nil
}

// decideBeginCombat server-rolls anyone still owing initiative, so one
// missing player can't stall the table.
func decideBeginCombat(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can begin combat")
	}
	e := s.Encounter
	if e == nil || e.Started() {
		return nil, reject(CodeInvalidTarget, "there is no combat waiting to begin")
	}
	var evs []Payload
	for _, id := range e.waiting() {
		entry := e.Order[e.index(id)]
		roll := env.Roll(20)
		evs = append(evs, InitiativeSet{Actor: id, Total: roll + entry.Bonus, Roll: &roll, Bonus: entry.Bonus, TieBreak: entry.TieBreak})
	}
	return appendFirstTurn(s, evs), nil
}

type joinCombatArgs struct {
	Actor ActorID `json:"actor"`
	Total *int    `json:"total"` // optional; rolled if missing
}

// decideJoinCombat adds a character mid-fight (a summon, a latecomer).
func decideJoinCombat(s *State, cmd Command, env Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can add combatants")
	}
	var a joinCombatArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	e := s.Encounter
	if e == nil {
		return nil, reject(CodeInvalidTarget, "there is no combat")
	}
	c := s.Actors[a.Actor]
	if c == nil {
		return nil, reject(CodeInvalidTarget, "no such character")
	}
	if e.index(a.Actor) >= 0 {
		return nil, reject(CodeInvalidTarget, c.Name+" is already in the fight")
	}
	ev := InitiativeSet{Actor: a.Actor, Bonus: c.InitBonus, TieBreak: env.Roll(20)}
	if a.Total != nil {
		ev.Total, ev.Physical = *a.Total, true
	} else {
		roll := env.Roll(20)
		ev.Roll, ev.Total = &roll, roll+c.InitBonus
	}
	return []Payload{ev}, nil
}

func decideRemoveFromCombat(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can remove combatants")
	}
	var a actorArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	e := s.Encounter
	if e.index(a.Actor) < 0 {
		return nil, reject(CodeInvalidTarget, "that character is not in the fight")
	}
	if e.Active == a.Actor {
		return nil, reject(CodeInvalidTarget, "end their turn first")
	}
	evs := []Payload{CombatantRemoved{Actor: a.Actor}}
	if len(e.Order) == 1 {
		evs = append(evs, CombatEnded{})
	}
	return appendFirstTurn(s, evs), nil
}

// activeTurn returns the encounter if turns have begun and user may act
// for the active actor (its controller, or the DM).
func activeTurn(s *State, user UserID) (*Encounter, error) {
	e := s.Encounter
	if !e.Started() {
		return nil, reject(CodeInvalidTarget, "no turn is in progress")
	}
	if !s.CanEdit(user, s.Actors[e.Active]) {
		return nil, reject(CodeNotYourTurn, "it is "+s.Actors[e.Active].Name+"'s turn")
	}
	return e, nil
}

func decideEndTurn(s *State, cmd Command, _ Env) ([]Payload, error) {
	e, err := activeTurn(s, cmd.By)
	if err != nil {
		return nil, err
	}
	i, round := e.index(e.Active)+1, e.Round
	if i == len(e.Order) {
		i, round = 0, round+1
	}
	next := e.Order[i].Actor
	return []Payload{
		TurnEnded{Actor: e.Active},
		TurnStarted{Actor: next, Round: round, Movement: s.Actors[next].Speed},
	}, nil
}

// decidePrevTurn is the DM going back one turn to fix a mistake; that
// actor gets a fresh turn.
func decidePrevTurn(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can go back a turn")
	}
	e := s.Encounter
	if !e.Started() {
		return nil, reject(CodeInvalidTarget, "no turn is in progress")
	}
	i, round := e.index(e.Active)-1, e.Round
	if i < 0 {
		if round == 1 {
			return nil, reject(CodeInvalidTarget, "this is the first turn")
		}
		i, round = len(e.Order)-1, round-1
	}
	prev := e.Order[i].Actor
	return []Payload{TurnStarted{Actor: prev, Round: round, Movement: s.Actors[prev].Speed}}, nil
}

type useActionArgs struct {
	Actor ActorID `json:"actor"` // for reactions; defaults to the active actor
	Kind  string  `json:"kind"`  // action, bonus, or reaction
	Used  *bool   `json:"used"`  // default true; false makes it available again
	Dash  bool    `json:"dash"`  // an action or bonus action used to Dash
}

func decideUseAction(s *State, cmd Command, _ Env) ([]Payload, error) {
	var a useActionArgs
	if err := decodeArgs(cmd, &a); err != nil {
		return nil, err
	}
	used := a.Used == nil || *a.Used
	e := s.Encounter
	if !e.Started() {
		return nil, reject(CodeInvalidTarget, "no turn is in progress")
	}

	if a.Kind == ActionReaction {
		if a.Actor == "" {
			a.Actor = e.Active
		}
		c, err := editable(s, cmd.By, a.Actor)
		if err != nil {
			return nil, err
		}
		if e.index(a.Actor) < 0 {
			return nil, reject(CodeInvalidTarget, c.Name+" is not in the fight")
		}
		if e.ReactionUsed[a.Actor] == used {
			return nil, reject(CodeInvalidTarget, c.Name+"'s reaction is already "+map[bool]string{true: "used", false: "available"}[used])
		}
		return []Payload{ActionUsed{Actor: a.Actor, Kind: a.Kind, Used: used}}, nil
	}

	if a.Kind != ActionMain && a.Kind != ActionBonus {
		return nil, reject(CodeInvalidTarget, "kind must be action, bonus, or reaction")
	}
	if a.Actor != "" && a.Actor != e.Active {
		return nil, reject(CodeNotYourTurn, "actions and bonus actions are only for the active character")
	}
	if _, err := activeTurn(s, cmd.By); err != nil {
		return nil, err
	}
	available, dashed := e.Economy.Action, e.Economy.ActionDash
	if a.Kind == ActionBonus {
		available, dashed = e.Economy.Bonus, e.Economy.BonusDash
	}
	if available != used {
		return nil, reject(CodeInvalidTarget, "that is already "+map[bool]string{true: "used", false: "available"}[used])
	}
	speed := s.Actors[e.Active].Speed
	left := e.Economy.MovementLeft
	switch {
	case used && a.Dash:
		left += speed
	case !used && dashed:
		left = max(0, left-speed) // take back the Dash
	}
	return []Payload{ActionUsed{Actor: e.Active, Kind: a.Kind, Used: used, Dash: used && a.Dash, MovementLeft: left}}, nil
}

func decideEndCombat(s *State, cmd Command, _ Env) ([]Payload, error) {
	if !s.IsDM(cmd.By) {
		return nil, reject(CodeForbidden, "only the DM can end combat")
	}
	if s.Encounter == nil {
		return nil, reject(CodeInvalidTarget, "there is no combat")
	}
	return []Payload{CombatEnded{}}, nil
}

// combatMove adds movement rules to a token move during combat. It returns
// the events for the move, or nil if combat doesn't govern this token.
func combatMove(s *State, by UserID, t *Token, to Cell) ([]Payload, error) {
	e := s.Encounter
	if !e.Started() || t.Actor == "" || e.index(t.Actor) < 0 {
		return nil, nil
	}
	moved := TokenMoved{Token: t.ID, From: t.Pos, To: to}
	left := e.Economy.MovementLeft
	cost, reachable := PathCost(s.Map, s.Settings.Diagonal, t.Size, t.Pos, to, -1)

	if s.IsDM(by) {
		// The DM is never refused. Moving the active character spends what
		// the move costs, up to what's left; anyone else moves for free.
		if e.Active != t.Actor || !reachable || cost == 0 {
			return []Payload{moved}, nil
		}
		spent := min(cost, left)
		return []Payload{moved, MovementSpent{Actor: t.Actor, Feet: spent, Left: left - spent}}, nil
	}
	if e.Active != t.Actor {
		return nil, reject(CodeNotYourTurn, "it is "+s.Actors[e.Active].Name+"'s turn")
	}
	if !reachable {
		return nil, reject(CodeInvalidTarget, "no way through: walls are in the way")
	}
	if cost > left {
		return nil, reject(CodeOutOfMovement, fmt.Sprintf("that is %d ft; you have %d ft left", cost, left))
	}
	return []Payload{moved, MovementSpent{Actor: t.Actor, Feet: cost, Left: left - cost}}, nil
}
