package game

import (
	"cmp"
	"maps"
	"slices"
)

// Viewer is who a view of the session is for.
type Viewer struct {
	User UserID
	DM   bool // sees everything
}

func (s *State) ViewerFor(user UserID) Viewer {
	return Viewer{User: user, DM: s.IsDM(user)}
}

// HiddenActor is the active actor in a player's view when it's the turn
// of a combatant they can't see.
const HiddenActor ActorID = "hidden"

// What players see, in brief:
//   - Tokens: not hidden ones, nor ones wholly in fog, except their own.
//   - Characters: every PC; a monster or NPC only while its token is
//     visible, and then masked (no stats, HP as healthy/bloodied/down).
//   - Initiative: only visible combatants; a hidden one's turn shows as
//     HiddenActor.
//   - Dice: no secret rolls. Fog: the party's and their own reveals.

func (s *State) controlsToken(u UserID, t *Token) bool {
	if slices.Contains(t.Controllers, u) {
		return true
	}
	a := s.Actors[t.Actor]
	return a != nil && slices.Contains(a.Controllers, u)
}

func (s *State) tokenVisible(v Viewer, t *Token) bool {
	switch {
	case v.DM || s.controlsToken(v.User, t):
		return true
	case t.Hidden:
		return false
	case s.Map == nil || !s.Map.Fog.Enabled:
		return true
	}
	for dy := range t.Size {
		for dx := range t.Size {
			if c := (Cell{t.Pos.X + dx, t.Pos.Y + dy}); s.Map.InBounds(c, 1) && s.Map.Revealed(v.User, c) {
				return true
			}
		}
	}
	return false
}

func (s *State) actorVisible(v Viewer, a *Character) bool {
	if v.DM || a.Kind == KindPC || slices.Contains(a.Controllers, v.User) {
		return true
	}
	t := s.TokenFor(a.ID)
	return t != nil && s.tokenVisible(v, t)
}

func (s *State) actorIDVisible(v Viewer, id ActorID) bool {
	a := s.Actors[id]
	return a != nil && s.actorVisible(v, a)
}

// masked reports whether v sees a's stats only coarsely.
func masked(v Viewer, a *Character) bool { return !v.DM && a.Kind != KindPC }

func maskCharacter(a *Character) *Character {
	return &Character{
		ID: a.ID, Kind: a.Kind, Name: a.Name,
		Conditions:  slices.Clone(a.Conditions),
		Controllers: slices.Clone(a.Controllers),
		Masked:      true,
		HPState:     a.HP.State(),
	}
}

// Visible is which tokens and characters a viewer can see. When it
// changes, the viewer is sent a fresh View instead of the event.
type Visible struct {
	Tokens []TokenID
	Actors []ActorID
}

func (s *State) Visible(v Viewer) Visible {
	var vis Visible
	for _, id := range slices.Sorted(maps.Keys(s.Tokens)) {
		if s.tokenVisible(v, s.Tokens[id]) {
			vis.Tokens = append(vis.Tokens, id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.Actors)) {
		if s.actorVisible(v, s.Actors[id]) {
			vis.Actors = append(vis.Actors, id)
		}
	}
	return vis
}

func (a Visible) Equal(b Visible) bool {
	return slices.Equal(a.Tokens, b.Tokens) && slices.Equal(a.Actors, b.Actors)
}

// View returns a copy of the state as v may see it. It shares nothing with
// s, so it can be sent, or applied to, freely.
func (s *State) View(v Viewer) *State {
	c := s.clone()
	if v.DM {
		return c
	}
	for id, t := range s.Tokens {
		if !s.tokenVisible(v, t) {
			delete(c.Tokens, id)
		}
	}
	for id, a := range s.Actors {
		switch {
		case !s.actorVisible(v, a):
			delete(c.Actors, id)
		case masked(v, a):
			c.Actors[id] = maskCharacter(a)
		}
	}
	if c.Map != nil {
		c.Map.Fog = c.Map.Fog.viewFor(v.User)
	}
	c.SecretRolls = []Roll{}
	if e := c.Encounter; e != nil {
		e.Order = slices.DeleteFunc(e.Order, func(x InitEntry) bool { return !s.actorIDVisible(v, x.Actor) })
		maps.DeleteFunc(e.ReactionUsed, func(id ActorID, _ bool) bool { return !s.actorIDVisible(v, id) })
		if e.Active != "" {
			switch a := s.Actors[e.Active]; {
			case !s.actorVisible(v, a):
				e.Active, e.Economy = HiddenActor, TurnEconomy{Action: true, Bonus: true}
			case masked(v, a):
				e.Economy.MovementLeft = 0 // would give away its speed
			}
		}
	}
	return c
}

// Project returns what v is sent for ev, which s already includes: ev
// itself, a version with hidden details removed, or a Hidden placeholder.
// It is only used when ev didn't change what v can see (see Visible);
// otherwise v gets a fresh View.
func Project(s *State, v Viewer, ev Event) Event {
	if v.DM {
		return ev
	}
	hide := Event{Seq: ev.Seq, Name: Hidden{}.EventName(), At: ev.At, Data: Hidden{}}
	with := func(p Payload) Event { ev.Data = p; return ev }
	actor := func(id ActorID) (*Character, bool) {
		a := s.Actors[id]
		return a, a != nil && s.actorVisible(v, a)
	}

	switch d := ev.Data.(type) {
	case MemberJoined, CellsPainted, SettingsChanged, FogSet, CombatEnded, Hidden:
		return ev
	case MapSet:
		d.Map.Fog = d.Map.Fog.viewFor(v.User)
		return with(d)
	case FogRevealed:
		if d.For == "" || d.For == v.User {
			return ev
		}
	case FogHidden:
		if d.For == "" || d.For == v.User {
			return ev
		}
	case TokenPlaced:
		if t := s.Tokens[d.Token.ID]; t != nil && s.tokenVisible(v, t) {
			return ev
		}
	case TokenMoved:
		if t := s.Tokens[d.Token]; t != nil && s.tokenVisible(v, t) {
			return ev
		}
	case TokenHidden:
		if t := s.Tokens[d.Token]; t != nil && s.tokenVisible(v, t) {
			return ev
		}
	case TokenRevealed:
		if t := s.Tokens[d.Token]; t != nil && s.tokenVisible(v, t) {
			return ev
		}
	case CharacterCreated:
		if a, ok := actor(d.Character.ID); ok {
			if masked(v, a) {
				return with(CharacterCreated{Character: *maskCharacter(a)})
			}
			return ev
		}
	case CharacterUpdated:
		if a, ok := actor(d.Character.ID); ok {
			if masked(v, a) {
				return with(CharacterUpdated{Character: *maskCharacter(a)})
			}
			return ev
		}
	case HPChanged:
		if a, ok := actor(d.Actor); ok {
			if masked(v, a) {
				return with(HPChanged{Actor: d.Actor, Delta: sign(d.Delta), HPState: a.HP.State()})
			}
			return ev
		}
	case ConditionAdded:
		if _, ok := actor(d.Actor); ok {
			return ev
		}
	case ConditionRemoved:
		if _, ok := actor(d.Actor); ok {
			return ev
		}
	case CombatStarted:
		order := slices.DeleteFunc(cloneEntries(d.Order), func(x InitEntry) bool { return !s.actorIDVisible(v, x.Actor) })
		return with(CombatStarted{Order: order})
	case InitiativeSet:
		if _, ok := actor(d.Actor); ok {
			return ev
		}
	case CombatantRemoved:
		if _, ok := actor(d.Actor); ok {
			return ev
		}
	case TurnStarted:
		a, ok := actor(d.Actor)
		switch {
		case !ok:
			return with(TurnStarted{Actor: HiddenActor, Round: d.Round})
		case masked(v, a):
			d.Movement = 0
			return with(d)
		}
		return ev
	case TurnEnded:
		if _, ok := actor(d.Actor); ok {
			return ev
		}
	case MovementSpent:
		if a, ok := actor(d.Actor); ok {
			if masked(v, a) {
				d.Feet, d.Left = 0, 0
				return with(d)
			}
			return ev
		}
	case ActionUsed:
		if a, ok := actor(d.Actor); ok {
			if masked(v, a) {
				d.MovementLeft = 0
				return with(d)
			}
			return ev
		}
	case DiceRolled:
		if !d.Secret {
			return ev
		}
	}
	// Anything else (removals of things v never saw, secret rolls, events
	// about hidden things, and any event type not listed) stays hidden.
	return hide
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// clone deep-copies the state.
func (s *State) clone() *State {
	c := *s
	if s.Map != nil {
		m := *s.Map
		m.Terrain = maps.Clone(s.Map.Terrain)
		m.Fog = s.Map.Fog.clone()
		c.Map = &m
	}
	c.Tokens = make(map[TokenID]*Token, len(s.Tokens))
	for id, t := range s.Tokens {
		tc := *t
		tc.Controllers = slices.Clone(t.Controllers)
		c.Tokens[id] = &tc
	}
	c.Actors = make(map[ActorID]*Character, len(s.Actors))
	for id, a := range s.Actors {
		c.Actors[id] = cloneCharacter(*a)
	}
	c.Members = make(map[UserID]*Member, len(s.Members))
	for id, m := range s.Members {
		mc := *m
		c.Members[id] = &mc
	}
	if s.Encounter != nil {
		e := *s.Encounter
		e.Order = cloneEntries(e.Order)
		e.ReactionUsed = maps.Clone(e.ReactionUsed)
		c.Encounter = &e
	}
	c.Rolls = slices.Clone(s.Rolls)
	c.SecretRolls = slices.Clone(s.SecretRolls)
	return &c
}

// Deliver returns what v is sent for ev, given what v could see before it:
// the event as Project shapes it, or ok=false if v needs a fresh View. A
// fresh view is needed when what v can see changed, unless the change is
// exactly the thing ev adds or removes (a token placed in sight, say).
func Deliver(s *State, v Viewer, ev Event, before Visible) (Event, bool) {
	after := s.Visible(v)
	if after.Equal(before) {
		return Project(s, v, ev), true
	}
	var tokens []TokenID
	var actors []ActorID
	added := true
	switch d := ev.Data.(type) {
	case TokenPlaced:
		tokens = []TokenID{d.Token.ID}
	case TokenRemoved:
		tokens, added = []TokenID{d.Token}, false
	case CharacterCreated:
		actors = []ActorID{d.Character.ID}
	case CharacterDeleted:
		actors, added = []ActorID{d.Actor}, false
	default:
		return Event{}, false
	}
	from, to := before, after
	if !added {
		from, to = after, before
	}
	if !slices.Equal(with(from.Tokens, tokens), to.Tokens) || !slices.Equal(with(from.Actors, actors), to.Actors) {
		return Event{}, false
	}
	if added {
		return Project(s, v, ev), true
	}
	return ev, true // v saw the thing that's gone
}

// with returns ids plus extra, sorted.
func with[T cmp.Ordered](ids, extra []T) []T {
	out := append(slices.Clone(ids), extra...)
	slices.Sort(out)
	return out
}
