package game

import (
	"errors"
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// dice returns an Env whose rolls come from faces in order.
func dice(t *testing.T, faces ...int) Env {
	return Env{
		NewID: testEnv.NewID,
		Roll: func(sides int) int {
			t.Helper()
			if len(faces) == 0 {
				t.Fatal("rolled more dice than the test provided")
			}
			f := faces[0]
			faces = faces[1:]
			if f < 1 || f > sides {
				t.Fatalf("test face %d is not on a d%d", f, sides)
			}
			return f
		},
	}
}

// arena is a 10x8 map with Kai (player, +3, speed 30), Ana (player, +1,
// rolls own dice, speed 25), and a goblin (DM, +2, speed 30) on it.
func arena() *State {
	s := withCharacters()
	s.Map.Terrain = Terrain{}
	s.Actors["act_ana"] = &Character{
		ID: "act_ana", Kind: KindPC, Name: "Ana", Speed: 25, InitBonus: 1, HP: HitPoints{Current: 10, Max: 10},
		Conditions: []Condition{}, Controllers: []UserID{ana}, RollsOwnDice: true,
	}
	s.Tokens["tok_ana"] = &Token{ID: "tok_ana", Actor: "act_ana", Label: "Ana", Pos: Cell{0, 2}, Size: 1, Controllers: []UserID{}}
	s.Tokens["tok_gob"] = &Token{ID: "tok_gob", Actor: goblin, Label: "Goblin", Pos: Cell{9, 7}, Size: 1, Controllers: []UserID{}}
	delete(s.Tokens, tok1)
	return s
}

// play decides c and applies its events, failing the test on a reject.
func play(t *testing.T, s *State, env Env, by UserID, name, args string) []Payload {
	t.Helper()
	evs, err := Decide(s, cmd(by, name, args), env)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	for _, p := range evs {
		s.Apply(Event{Seq: s.Seq + 1, Name: p.EventName(), Data: p})
	}
	return evs
}

func refuse(t *testing.T, s *State, env Env, by UserID, name, args, code string) {
	t.Helper()
	_, err := Decide(s, cmd(by, name, args), env)
	var r *Reject
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("%s %s by %s: got %v, want %s", name, args, by, err, code)
	}
}

func order(s *State) []ActorID {
	var ids []ActorID
	for _, e := range s.Encounter.Order {
		ids = append(ids, e.Actor)
	}
	return ids
}

func TestInitiativeOrder(t *testing.T) {
	e := &Encounter{Order: []InitEntry{
		{Actor: "waiting", Bonus: 9},
		{Actor: "low", Total: ptr(5), Bonus: 0},
		{Actor: "tie-low-bonus", Total: ptr(15), Bonus: 1, TieBreak: 20},
		{Actor: "tie-high-bonus", Total: ptr(15), Bonus: 3, TieBreak: 1},
		{Actor: "tie-same-bonus-b", Total: ptr(15), Bonus: 1, TieBreak: 12},
		{Actor: "high", Total: ptr(22), Bonus: -1},
	}}
	e.sortOrder()
	want := []ActorID{"high", "tie-high-bonus", "tie-low-bonus", "tie-same-bonus-b", "low", "waiting"}
	if got := order(&State{Encounter: e}); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
}

func TestStartCombatWaitsForPhysicalRolls(t *testing.T) {
	s := arena()
	refuse(t, s, testEnv, kai, "start_combat", `{}`, CodeForbidden)

	// Participants sorted by ID: act_ana, act_gob, act_kai. Each gets a
	// tie-break roll; Ana rolls her own die, so only the others roll d20s.
	evs := play(t, s, dice(t, 5, 3, 14, 7, 10), dm, "start_combat", ``)
	if len(evs) != 1 {
		t.Fatalf("turns began while Ana's roll is missing: %+v", evs)
	}
	if got := order(s); !reflect.DeepEqual(got, []ActorID{"act_gob", "act_kai", "act_ana"}) {
		t.Fatalf("order = %v (goblin 14+2=16, kai 10+3=13, ana waiting)", got)
	}
	if s.Encounter.Started() {
		t.Fatal("started without Ana's initiative")
	}
	refuse(t, s, testEnv, dm, "start_combat", ``, CodeInvalidTarget)
	refuse(t, s, testEnv, kai, "end_turn", `{}`, CodeInvalidTarget)

	// Players enter only their own roll, within what a d20 can show.
	refuse(t, s, testEnv, kai, "set_initiative", `{"actor":"act_ana","roll":12}`, CodeForbidden)
	refuse(t, s, testEnv, ana, "set_initiative", `{"actor":"act_ana","roll":21}`, CodeInvalidTarget)
	refuse(t, s, testEnv, ana, "set_initiative", `{"actor":"act_ana","total":22}`, CodeInvalidTarget)
	refuse(t, s, testEnv, ana, "set_initiative", `{"actor":"act_ana"}`, CodeInvalidTarget)

	// Ana's 19 + 1 = 20 puts her first, and her entry starts round 1.
	evs = play(t, s, testEnv, ana, "set_initiative", `{"actor":"act_ana","roll":19}`)
	want := []Payload{
		InitiativeSet{Actor: "act_ana", Total: 20, Roll: ptr(19), Bonus: 1, TieBreak: 5, Physical: true},
		TurnStarted{Actor: "act_ana", Round: 1, Movement: 25},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("got  %+v\nwant %+v", evs, want)
	}
	if got := order(s); !reflect.DeepEqual(got, []ActorID{"act_ana", "act_gob", "act_kai"}) {
		t.Fatalf("order = %v", got)
	}
	// Once set, only the DM can change it.
	refuse(t, s, testEnv, ana, "set_initiative", `{"actor":"act_ana","roll":2}`, CodeForbidden)
}

func TestStartCombatWithNoPhysicalRollsBeginsAtOnce(t *testing.T) {
	s := arena()
	// Kai: tie-break 1, d20 18 (+3 = 21). Goblin: tie-break 2, d20 11 (+2 = 13).
	evs := play(t, s, dice(t, 1, 18, 2, 11), dm, "start_combat", `{"actors":["act_kai","act_gob"]}`)
	if len(evs) != 2 || evs[1] != (TurnStarted{Actor: kaiPC, Round: 1, Movement: 30}) {
		t.Fatalf("events = %+v, want CombatStarted then Kai's turn (18+3 beats 11+2)", evs)
	}
	e := s.Encounter
	if e.Economy != (TurnEconomy{MovementLeft: 30, Action: true, Bonus: true}) {
		t.Fatalf("economy = %+v", e.Economy)
	}
}

func TestBeginCombatRollsForTheMissing(t *testing.T) {
	s := arena()
	play(t, s, dice(t, 5, 3, 14, 7, 10), dm, "start_combat", ``)
	refuse(t, s, testEnv, kai, "begin_combat", ``, CodeForbidden)
	evs := play(t, s, dice(t, 2), dm, "begin_combat", ``)
	if len(evs) != 2 || evs[0].(InitiativeSet).Physical || evs[0].(InitiativeSet).Total != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if !s.Encounter.Started() || s.Encounter.Active != goblin {
		t.Fatalf("active = %q", s.Encounter.Active)
	}
	refuse(t, s, testEnv, dm, "begin_combat", ``, CodeInvalidTarget)
}

// fight starts combat with the order goblin (16), Kai (13), Ana (4).
func fight(t *testing.T) *State {
	s := arena()
	play(t, s, dice(t, 5, 3, 14, 7, 10, 3), dm, "start_combat", ``)
	play(t, s, dice(t, 3), dm, "begin_combat", ``)
	return s
}

func TestTurns(t *testing.T) {
	s := fight(t)
	e := s.Encounter
	if e.Active != goblin || e.Round != 1 {
		t.Fatalf("active %s round %d", e.Active, e.Round)
	}
	refuse(t, s, testEnv, kai, "end_turn", `{}`, CodeNotYourTurn)

	// Kai uses his reaction on the goblin's turn.
	play(t, s, testEnv, kai, "use_action", `{"actor":"act_kai","kind":"reaction"}`)
	refuse(t, s, testEnv, kai, "use_action", `{"actor":"act_kai","kind":"reaction"}`, CodeInvalidTarget)
	refuse(t, s, testEnv, kai, "use_action", `{"actor":"act_gob","kind":"reaction"}`, CodeForbidden)

	play(t, s, testEnv, dm, "end_turn", `{}`)
	if e.Active != kaiPC || e.Round != 1 || e.ReactionUsed[kaiPC] {
		t.Fatalf("Kai's turn: active %s round %d, reaction used %v (resets on his turn)", e.Active, e.Round, e.ReactionUsed[kaiPC])
	}
	play(t, s, testEnv, ana, "use_action", `{"actor":"act_ana","kind":"reaction"}`)
	if !e.ReactionUsed["act_ana"] {
		t.Fatal("Ana's reaction not marked used")
	}
	play(t, s, testEnv, kai, "end_turn", `{}`)
	if e.Active != "act_ana" || e.ReactionUsed["act_ana"] {
		t.Fatal("Ana's reaction did not reset at the start of her turn")
	}
	play(t, s, testEnv, ana, "end_turn", `{}`)
	if e.Active != goblin || e.Round != 2 {
		t.Fatalf("after Ana: active %s round %d, want goblin in round 2", e.Active, e.Round)
	}

	// The DM can step back, across the round boundary.
	refuse(t, s, testEnv, kai, "prev_turn", `{}`, CodeForbidden)
	play(t, s, testEnv, dm, "prev_turn", `{}`)
	if e.Active != "act_ana" || e.Round != 1 || e.Economy.MovementLeft != 25 {
		t.Fatalf("prev: active %s round %d economy %+v", e.Active, e.Round, e.Economy)
	}
	play(t, s, testEnv, dm, "prev_turn", `{}`)
	play(t, s, testEnv, dm, "prev_turn", `{}`)
	refuse(t, s, testEnv, dm, "prev_turn", `{}`, CodeInvalidTarget)

	play(t, s, testEnv, dm, "end_combat", `{}`)
	if s.Encounter != nil {
		t.Fatal("combat still running")
	}
}

func TestActionEconomy(t *testing.T) {
	s := fight(t)
	play(t, s, testEnv, dm, "end_turn", `{}`) // Kai's turn
	e := s.Encounter

	refuse(t, s, testEnv, ana, "use_action", `{"kind":"action"}`, CodeNotYourTurn)
	refuse(t, s, testEnv, kai, "use_action", `{"kind":"shove"}`, CodeInvalidTarget)
	refuse(t, s, testEnv, kai, "use_action", `{"kind":"action","used":false}`, CodeInvalidTarget)

	play(t, s, testEnv, kai, "use_action", `{"kind":"action"}`)
	refuse(t, s, testEnv, kai, "use_action", `{"kind":"action"}`, CodeInvalidTarget)
	play(t, s, testEnv, kai, "use_action", `{"kind":"bonus","dash":true}`) // Cunning Action: Dash
	if e.Economy != (TurnEconomy{MovementLeft: 60, Action: false, Bonus: false, BonusDash: true}) {
		t.Fatalf("economy = %+v", e.Economy)
	}
	// Undoing the Dash takes the extra movement back.
	play(t, s, testEnv, kai, "use_action", `{"kind":"bonus","used":false}`)
	if e.Economy.MovementLeft != 30 || !e.Economy.Bonus || e.Economy.BonusDash {
		t.Fatalf("economy after undo = %+v", e.Economy)
	}
	play(t, s, testEnv, kai, "use_action", `{"kind":"action","used":false}`)
	if !e.Economy.Action {
		t.Fatal("action not restored")
	}
}

func TestCombatMovement(t *testing.T) {
	s := fight(t)
	e := s.Encounter
	// Goblin's turn: Kai can't move.
	refuse(t, s, testEnv, kai, "move_token", `{"token":"tok_kai","to":{"x":1,"y":0}}`, CodeNotYourTurn)
	// The DM can reposition Kai for free.
	play(t, s, testEnv, dm, "move_token", `{"token":"tok_kai","to":{"x":0,"y":1}}`)
	if e.Economy.MovementLeft != 30 {
		t.Fatalf("moving another token spent the goblin's movement: %+v", e.Economy)
	}
	// Moving the goblin on its turn spends its movement, even for the DM.
	play(t, s, testEnv, dm, "move_token", `{"token":"tok_gob","to":{"x":7,"y":7}}`)
	if e.Economy.MovementLeft != 20 {
		t.Fatalf("goblin movement left = %d, want 20", e.Economy.MovementLeft)
	}
	// Past its movement, the DM's move still goes through and uses up the rest.
	play(t, s, testEnv, dm, "move_token", `{"token":"tok_gob","to":{"x":0,"y":7}}`)
	if e.Economy.MovementLeft != 0 {
		t.Fatalf("goblin movement left = %d, want 0", e.Economy.MovementLeft)
	}

	play(t, s, testEnv, dm, "end_turn", `{}`) // Kai at (0,1), 30 ft
	evs := play(t, s, testEnv, kai, "move_token", `{"token":"tok_kai","to":{"x":4,"y":1}}`)
	if !reflect.DeepEqual(evs[1], MovementSpent{Actor: kaiPC, Feet: 20, Left: 10}) {
		t.Fatalf("events = %+v", evs)
	}
	refuse(t, s, testEnv, kai, "move_token", `{"token":"tok_kai","to":{"x":7,"y":1}}`, CodeOutOfMovement)
	play(t, s, testEnv, kai, "use_action", `{"kind":"action","dash":true}`)
	play(t, s, testEnv, kai, "move_token", `{"token":"tok_kai","to":{"x":7,"y":1}}`)
	if e.Economy.MovementLeft != 25 {
		t.Fatalf("movement left after dash and move = %d, want 25", e.Economy.MovementLeft)
	}

	// Under 5-10-5, three diagonals cost 20 ft.
	play(t, s, testEnv, dm, "set_settings", `{"diagonal":"5-10-5"}`)
	play(t, s, testEnv, kai, "move_token", `{"token":"tok_kai","to":{"x":4,"y":4}}`)
	if e.Economy.MovementLeft != 5 {
		t.Fatalf("movement left = %d, want 5", e.Economy.MovementLeft)
	}

	// Tokens that aren't in the fight move freely, still around walls.
	s.Tokens["tok_crate"] = &Token{ID: "tok_crate", Label: "Crate", Pos: Cell{5, 5}, Size: 1, Controllers: []UserID{ana}}
	play(t, s, testEnv, ana, "move_token", `{"token":"tok_crate","to":{"x":0,"y":0}}`)
}

func TestCombatantsComeAndGo(t *testing.T) {
	s := fight(t) // goblin, Kai, Ana
	e := s.Encounter
	s.Actors["act_wolf"] = &Character{ID: "act_wolf", Name: "Wolf", Speed: 40, InitBonus: 2, Conditions: []Condition{}, Controllers: []UserID{kai}}

	refuse(t, s, testEnv, kai, "join_combat", `{"actor":"act_wolf"}`, CodeForbidden)
	play(t, s, dice(t, 1, 12), dm, "join_combat", `{"actor":"act_wolf"}`) // 12+2 = 14: after the goblin, before Kai
	if got := order(s); !reflect.DeepEqual(got, []ActorID{goblin, "act_wolf", kaiPC, "act_ana"}) {
		t.Fatalf("order = %v", got)
	}
	refuse(t, s, testEnv, dm, "join_combat", `{"actor":"act_wolf"}`, CodeInvalidTarget)

	// Changing initiative mid-fight re-sorts but keeps the active turn.
	play(t, s, testEnv, dm, "set_initiative", `{"actor":"act_ana","total":30}`)
	if e.Active != goblin || order(s)[0] != "act_ana" {
		t.Fatalf("active %s, order %v", e.Active, order(s))
	}

	refuse(t, s, testEnv, dm, "remove_from_combat", `{"actor":"act_gob"}`, CodeInvalidTarget) // it's the goblin's turn
	play(t, s, testEnv, dm, "remove_from_combat", `{"actor":"act_wolf"}`)
	refuse(t, s, testEnv, dm, "delete_character", `{"actor":"act_gob"}`, CodeInvalidTarget)
	play(t, s, testEnv, dm, "delete_character", `{"actor":"act_ana"}`)
	if got := order(s); !reflect.DeepEqual(got, []ActorID{goblin, kaiPC}) {
		t.Fatalf("order = %v", got)
	}
	play(t, s, testEnv, dm, "end_turn", `{}`)
	play(t, s, testEnv, dm, "remove_from_combat", `{"actor":"act_gob"}`)
	evs := play(t, s, testEnv, dm, "delete_character", `{"actor":"act_gob"}`)
	if len(evs) != 2 { // token and character; the goblin already left the fight
		t.Fatalf("events = %+v", evs)
	}
	if e := s.Encounter; e == nil || len(e.Order) != 1 {
		t.Fatalf("encounter = %+v", e)
	}
}

// Deleting the last character we were waiting on starts the first turn.
func TestDeletingTheLastWaitingCombatantStartsTurns(t *testing.T) {
	s := arena()
	play(t, s, dice(t, 5, 3, 14, 7, 10), dm, "start_combat", ``)
	evs := play(t, s, testEnv, dm, "delete_character", `{"actor":"act_ana"}`)
	if last := evs[len(evs)-1]; last != (TurnStarted{Actor: goblin, Round: 1, Movement: 30}) {
		t.Fatalf("events = %+v", evs)
	}
}
