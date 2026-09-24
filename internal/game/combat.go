package game

import (
	"cmp"
	"slices"
)

// Encounter is a fight in progress.
type Encounter struct {
	Round int         `json:"round"`
	Order []InitEntry `json:"order"` // sorted: see sortOrder
	// Active is whose turn it is; empty while waiting for initiative rolls
	// before the first turn.
	Active       ActorID          `json:"active"`
	Economy      TurnEconomy      `json:"economy"`       // the active actor's
	ReactionUsed map[ActorID]bool `json:"reaction_used"` // reset at the start of each actor's own turn
}

type InitEntry struct {
	Actor    ActorID `json:"actor"`
	Total    *int    `json:"total"` // nil while waiting for a physical roll
	Roll     *int    `json:"roll"`  // the d20 face, when known
	Bonus    int     `json:"bonus"`
	TieBreak int     `json:"tie_break"` // server d20 that orders otherwise-equal entries
	Physical bool    `json:"physical"`  // entered from a real die
}

type TurnEconomy struct {
	MovementLeft int  `json:"movement_left"` // feet
	Action       bool `json:"action"`        // true = still available
	Bonus        bool `json:"bonus"`
	ActionDash   bool `json:"action_dash"` // the action was used to Dash
	BonusDash    bool `json:"bonus_dash"`
}

// Started reports whether turns have begun (all initiative is in).
func (e *Encounter) Started() bool { return e != nil && e.Active != "" }

func (e *Encounter) index(a ActorID) int {
	if e == nil {
		return -1
	}
	return slices.IndexFunc(e.Order, func(x InitEntry) bool { return x.Actor == a })
}

// sortOrder sorts by total (waiting entries last), then initiative bonus,
// then the tie-break roll, then ID so the order is always deterministic.
func (e *Encounter) sortOrder() {
	slices.SortStableFunc(e.Order, func(a, b InitEntry) int {
		if (a.Total == nil) != (b.Total == nil) {
			if a.Total == nil {
				return 1
			}
			return -1
		}
		if a.Total != nil {
			if c := cmp.Compare(*b.Total, *a.Total); c != 0 {
				return c
			}
		}
		return cmp.Or(cmp.Compare(b.Bonus, a.Bonus), cmp.Compare(b.TieBreak, a.TieBreak), cmp.Compare(a.Actor, b.Actor))
	})
}

func (e *Encounter) remove(a ActorID) {
	if i := e.index(a); i >= 0 {
		e.Order = slices.Delete(e.Order, i, i+1)
	}
}

// waiting lists actors still owing an initiative roll.
func (e *Encounter) waiting() []ActorID {
	var w []ActorID
	for _, x := range e.Order {
		if x.Total == nil {
			w = append(w, x.Actor)
		}
	}
	return w
}

func cloneEntries(es []InitEntry) []InitEntry {
	out := slices.Clone(es)
	for i := range out {
		if out[i].Total != nil {
			t := *out[i].Total
			out[i].Total = &t
		}
		if out[i].Roll != nil {
			r := *out[i].Roll
			out[i].Roll = &r
		}
	}
	return out
}

// applyCombat folds combat events into s.
func (s *State) applyCombat(ev Payload) {
	switch d := ev.(type) {
	case CombatStarted:
		s.Encounter = &Encounter{Round: 1, Order: cloneEntries(d.Order), ReactionUsed: map[ActorID]bool{}}
		s.Encounter.sortOrder()
	case InitiativeSet:
		e := s.Encounter
		if e == nil {
			return
		}
		entry := InitEntry{Actor: d.Actor, Total: &d.Total, Roll: d.Roll, Bonus: d.Bonus, TieBreak: d.TieBreak, Physical: d.Physical}
		entry = cloneEntries([]InitEntry{entry})[0]
		if i := e.index(d.Actor); i >= 0 {
			entry.TieBreak = e.Order[i].TieBreak
			e.Order[i] = entry
		} else {
			e.Order = append(e.Order, entry) // joining mid-fight
		}
		e.sortOrder()
	case CombatantRemoved:
		s.Encounter.remove(d.Actor)
	case CharacterDeleted:
		s.Encounter.remove(d.Actor)
	case TurnStarted:
		e := s.Encounter
		if e == nil {
			return
		}
		e.Active, e.Round = d.Actor, d.Round
		e.Economy = TurnEconomy{MovementLeft: d.Movement, Action: true, Bonus: true}
		delete(e.ReactionUsed, d.Actor)
	case MovementSpent:
		if e := s.Encounter; e != nil && e.Active == d.Actor {
			e.Economy.MovementLeft = d.Left
		}
	case ActionUsed:
		e := s.Encounter
		if e == nil {
			return
		}
		switch d.Kind {
		case ActionReaction:
			if d.Used {
				e.ReactionUsed[d.Actor] = true
			} else {
				delete(e.ReactionUsed, d.Actor)
			}
		case ActionMain, ActionBonus:
			if e.Active != d.Actor {
				return
			}
			e.Economy.MovementLeft = d.MovementLeft
			if d.Kind == ActionMain {
				e.Economy.Action, e.Economy.ActionDash = !d.Used, d.Used && d.Dash
			} else {
				e.Economy.Bonus, e.Economy.BonusDash = !d.Used, d.Used && d.Dash
			}
		}
	case CombatEnded:
		s.Encounter = nil
	}
}

// Action kinds for use_action.
const (
	ActionMain     = "action"
	ActionBonus    = "bonus"
	ActionReaction = "reaction"
)

type CombatStarted struct {
	Order []InitEntry `json:"order"`
}

// InitiativeSet records an initiative total: entered, rolled late, or
// changed by the DM. An actor not yet in the fight joins it.
type InitiativeSet struct {
	Actor    ActorID `json:"actor"`
	Total    int     `json:"total"`
	Roll     *int    `json:"roll,omitempty"`
	Bonus    int     `json:"bonus"`
	TieBreak int     `json:"tie_break"`
	Physical bool    `json:"physical"`
}

type CombatantRemoved struct {
	Actor ActorID `json:"actor"`
}

type TurnStarted struct {
	Actor    ActorID `json:"actor"`
	Round    int     `json:"round"`
	Movement int     `json:"movement"` // feet available this turn
}

type TurnEnded struct {
	Actor ActorID `json:"actor"`
}

type MovementSpent struct {
	Actor ActorID `json:"actor"`
	Feet  int     `json:"feet"`
	Left  int     `json:"left"`
}

// ActionUsed marks an action, bonus action, or reaction used (or, with
// Used false, available again, to fix a misclick). A Dash adds the actor's
// speed to this turn's movement; MovementLeft is the result.
type ActionUsed struct {
	Actor        ActorID `json:"actor"`
	Kind         string  `json:"kind"`
	Used         bool    `json:"used"`
	Dash         bool    `json:"dash,omitempty"`
	MovementLeft int     `json:"movement_left"`
}

type CombatEnded struct{}

func (CombatStarted) EventName() string    { return "CombatStarted" }
func (InitiativeSet) EventName() string    { return "InitiativeSet" }
func (CombatantRemoved) EventName() string { return "CombatantRemoved" }
func (TurnStarted) EventName() string      { return "TurnStarted" }
func (TurnEnded) EventName() string        { return "TurnEnded" }
func (MovementSpent) EventName() string    { return "MovementSpent" }
func (ActionUsed) EventName() string       { return "ActionUsed" }
func (CombatEnded) EventName() string      { return "CombatEnded" }
