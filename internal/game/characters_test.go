package game

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

const (
	kaiPC  ActorID = "act_kai"
	goblin ActorID = "act_gob"
)

// withCharacters adds Kai's PC (with a token) and a DM-controlled goblin.
func withCharacters() *State {
	s := fixture()
	s.Actors[kaiPC] = &Character{
		ID: kaiPC, Kind: KindPC, Name: "Kai", Level: 3, AC: 15, Speed: 30, InitBonus: 3,
		HP: HitPoints{Current: 20, Max: 24}, Conditions: []Condition{}, Controllers: []UserID{kai},
	}
	s.Actors[goblin] = &Character{
		ID: goblin, Kind: KindMonster, Name: "Goblin", AC: 15, Speed: 30, InitBonus: 2,
		HP: HitPoints{Current: 7, Max: 7}, Conditions: []Condition{{Name: "Prone"}}, Controllers: []UserID{},
	}
	s.Tokens["tok_kai"] = &Token{ID: "tok_kai", Actor: kaiPC, Label: "Kai", Pos: Cell{0, 0}, Size: 1, Controllers: []UserID{}}
	return s
}

func decide(t *testing.T, s *State, c Command) ([]Payload, *Reject) {
	t.Helper()
	evs, err := Decide(s, c, testEnv)
	var r *Reject
	if err != nil && !errors.As(err, &r) {
		t.Fatalf("non-reject error %v", err)
	}
	return evs, r
}

// Who may do what to a character: the DM anything, a player only their own.
func TestCharacterPermissions(t *testing.T) {
	cmds := []struct{ name, args string }{
		{"update_character", `{"actor":"%s","ac":16}`},
		{"adjust_hp", `{"actor":"%s","delta":-1}`},
		{"set_temp_hp", `{"actor":"%s","temp":5}`},
		{"add_condition", `{"actor":"%s","name":"Blinded"}`},
		{"delete_character", `{"actor":"%s"}`},
	}
	for _, c := range cmds {
		for _, tt := range []struct {
			by    UserID
			actor ActorID
			ok    bool
		}{
			{dm, kaiPC, true},
			{dm, goblin, true},
			{kai, kaiPC, c.name != "delete_character"}, // only the DM deletes
			{kai, goblin, false},
			{ana, kaiPC, false},
		} {
			t.Run(c.name+"/"+string(tt.by)+"/"+string(tt.actor), func(t *testing.T) {
				_, r := decide(t, withCharacters(), cmd(tt.by, c.name, fmt.Sprintf(c.args, tt.actor)))
				if tt.ok && r != nil {
					t.Fatalf("rejected: %v", r)
				}
				if !tt.ok && (r == nil || r.Code != CodeForbidden) {
					t.Fatalf("got %v, want forbidden", r)
				}
			})
		}
	}
}

func TestCreateCharacter(t *testing.T) {
	t.Run("player makes their own PC with defaults", func(t *testing.T) {
		evs, r := decide(t, withCharacters(), cmd(ana, "create_character", `{"name":" Ana ","class":"Cleric","max_hp":18}`))
		if r != nil {
			t.Fatal(r)
		}
		want := CharacterCreated{Character: Character{
			ID: "act_new", Kind: KindPC, Name: "Ana", Class: "Cleric", Level: 1, AC: 10, Speed: 30,
			HP: HitPoints{Current: 18, Max: 18}, Conditions: []Condition{}, Controllers: []UserID{ana},
		}}
		if !reflect.DeepEqual(evs, []Payload{want}) {
			t.Fatalf("got  %+v\nwant %+v", evs, want)
		}
	})
	t.Run("DM makes a monster by default", func(t *testing.T) {
		evs, r := decide(t, withCharacters(), cmd(dm, "create_character", `{"name":"Ogre","max_hp":59,"ac":11,"speed":40,"init_bonus":-1}`))
		if r != nil {
			t.Fatal(r)
		}
		c := evs[0].(CharacterCreated).Character
		if c.Kind != KindMonster || c.HP != (HitPoints{Current: 59, Max: 59}) || c.Speed != 40 || c.InitBonus != -1 || len(c.Controllers) != 0 {
			t.Fatalf("ogre = %+v", c)
		}
	})
	t.Run("DM makes a companion NPC for a player", func(t *testing.T) {
		evs, r := decide(t, withCharacters(), cmd(dm, "create_character", `{"kind":"npc","name":"Wolf","max_hp":11,"controllers":["usr_ana"]}`))
		if r != nil {
			t.Fatal(r)
		}
		if c := evs[0].(CharacterCreated).Character; c.Kind != KindNPC || !reflect.DeepEqual(c.Controllers, []UserID{ana}) {
			t.Fatalf("wolf = %+v", c)
		}
	})

	for _, tt := range []struct {
		name string
		by   UserID
		args string
		code string
	}{
		{"player makes a monster", kai, `{"kind":"monster","name":"X","max_hp":1}`, CodeForbidden},
		{"player picks controllers", kai, `{"name":"X","max_hp":1,"controllers":["usr_ana"]}`, CodeForbidden},
		{"no name", dm, `{"max_hp":5}`, CodeInvalidTarget},
		{"no max HP", dm, `{"name":"X"}`, CodeInvalidTarget},
		{"blank name", dm, `{"name":"  ","max_hp":5}`, CodeInvalidTarget},
		{"zero max HP", dm, `{"name":"X","max_hp":0}`, CodeInvalidTarget},
		{"AC too high", dm, `{"name":"X","max_hp":5,"ac":41}`, CodeInvalidTarget},
		{"negative speed", dm, `{"name":"X","max_hp":5,"speed":-5}`, CodeInvalidTarget},
		{"bad kind", dm, `{"kind":"dragon","name":"X","max_hp":5}`, CodeInvalidTarget},
		{"unknown controller", dm, `{"name":"X","max_hp":5,"controllers":["usr_ghost"]}`, CodeInvalidTarget},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, r := decide(t, withCharacters(), cmd(tt.by, "create_character", tt.args)); r == nil || r.Code != tt.code {
				t.Fatalf("got %v, want %s", r, tt.code)
			}
		})
	}
}

func TestUpdateCharacter(t *testing.T) {
	evs, r := decide(t, withCharacters(), cmd(kai, "update_character", `{"actor":"act_kai","level":4,"max_hp":15,"rolls_own_dice":true}`))
	if r != nil {
		t.Fatal(r)
	}
	c := evs[0].(CharacterUpdated).Character
	if c.Level != 4 || c.HP != (HitPoints{Current: 15, Max: 15}) || !c.RollsOwnDice || c.Name != "Kai" || c.AC != 15 {
		t.Fatalf("updated = %+v (lowering max HP should cap current)", c)
	}
	if _, r := decide(t, withCharacters(), cmd(kai, "update_character", `{"actor":"act_kai","controllers":["usr_ana"]}`)); r == nil || r.Code != CodeForbidden {
		t.Fatalf("player changed controllers: %v", r)
	}
	if _, r := decide(t, withCharacters(), cmd(kai, "update_character", `{"actor":"act_kai","kind":"monster"}`)); r == nil || r.Code != CodeForbidden {
		t.Fatalf("player changed kind: %v", r)
	}
}

func TestHitPoints(t *testing.T) {
	tests := []struct {
		name string
		hp   HitPoints
		dmg  int // negative heals
		want HitPoints
	}{
		{"damage", HitPoints{20, 24, 0}, 5, HitPoints{15, 24, 0}},
		{"temp absorbs first", HitPoints{20, 24, 3}, 5, HitPoints{18, 24, 0}},
		{"temp absorbs all", HitPoints{20, 24, 8}, 5, HitPoints{20, 24, 3}},
		{"never below 0", HitPoints{4, 24, 0}, 30, HitPoints{0, 24, 0}},
		{"heal", HitPoints{10, 24, 2}, -5, HitPoints{15, 24, 2}},
		{"heal caps at max", HitPoints{22, 24, 0}, -10, HitPoints{24, 24, 0}},
		{"heal from 0", HitPoints{0, 24, 0}, -1, HitPoints{1, 24, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.hp.Heal(-tt.dmg)
			if tt.dmg > 0 {
				got = tt.hp.Damage(tt.dmg)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHPCommands(t *testing.T) {
	s := withCharacters()
	s.Actors[kaiPC].HP.Temp = 4
	for _, tt := range []struct {
		name, cmd, args string
		want            HitPoints
	}{
		{"damage through temp", "adjust_hp", `{"actor":"act_kai","delta":-10}`, HitPoints{14, 24, 0}},
		{"heal", "adjust_hp", `{"actor":"act_kai","delta":100}`, HitPoints{24, 24, 4}},
		{"lower temp HP is ignored", "set_temp_hp", `{"actor":"act_kai","temp":2}`, HitPoints{20, 24, 4}},
		{"higher temp HP replaces", "set_temp_hp", `{"actor":"act_kai","temp":9}`, HitPoints{20, 24, 9}},
		{"zero clears temp HP", "set_temp_hp", `{"actor":"act_kai","temp":0}`, HitPoints{20, 24, 0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evs, r := decide(t, s, cmd(dm, tt.cmd, tt.args))
			if r != nil {
				t.Fatal(r)
			}
			if got := evs[0].(HPChanged).HP; got != tt.want {
				t.Fatalf("HP = %+v, want %+v", got, tt.want)
			}
		})
	}
	for _, args := range []string{`{"actor":"act_kai","delta":0}`, `{"actor":"act_kai","delta":10000}`, `{"actor":"act_nope","delta":1}`} {
		if _, r := decide(t, s, cmd(dm, "adjust_hp", args)); r == nil || r.Code != CodeInvalidTarget {
			t.Errorf("%s: got %v, want invalid_target", args, r)
		}
	}
}

func TestConditions(t *testing.T) {
	s := withCharacters()
	evs, r := decide(t, s, cmd(dm, "add_condition", `{"actor":"act_gob","name":" Frightened ","source":"Kai's Menace"}`))
	if r != nil || !reflect.DeepEqual(evs, []Payload{ConditionAdded{Actor: goblin, Condition: Condition{Name: "Frightened", Source: "Kai's Menace"}}}) {
		t.Fatalf("add = %+v, %v", evs, r)
	}
	if _, r := decide(t, s, cmd(dm, "add_condition", `{"actor":"act_gob","name":"prone"}`)); r == nil {
		t.Fatal("added a condition the goblin already has")
	}
	evs, r = decide(t, s, cmd(dm, "remove_condition", `{"actor":"act_gob","name":"PRONE"}`))
	if r != nil || !reflect.DeepEqual(evs, []Payload{ConditionRemoved{Actor: goblin, Name: "Prone"}}) {
		t.Fatalf("remove = %+v, %v", evs, r)
	}
	if _, r := decide(t, s, cmd(dm, "remove_condition", `{"actor":"act_gob","name":"Stunned"}`)); r == nil {
		t.Fatal("removed a condition the goblin doesn't have")
	}

	s.Apply(Event{Seq: 1, Data: ConditionAdded{Actor: goblin, Condition: Condition{Name: "Frightened"}}})
	s.Apply(Event{Seq: 2, Data: ConditionRemoved{Actor: goblin, Name: "Prone"}})
	if got := s.Actors[goblin].Conditions; !reflect.DeepEqual(got, []Condition{{Name: "Frightened"}}) {
		t.Fatalf("conditions = %+v", got)
	}
}

func TestCharacterTokens(t *testing.T) {
	s := withCharacters()
	evs, r := decide(t, s, cmd(dm, "place_token", `{"actor":"act_gob","at":{"x":3,"y":3}}`))
	if r != nil {
		t.Fatal(r)
	}
	tok := evs[0].(TokenPlaced).Token
	if tok.Actor != goblin || tok.Label != "Goblin" || tok.Color != kindColors[KindMonster] {
		t.Fatalf("goblin token = %+v", tok)
	}

	if _, r := decide(t, s, cmd(dm, "place_token", `{"actor":"act_kai","at":{"x":3,"y":3}}`)); r == nil {
		t.Fatal("placed a second token for Kai")
	}
	if _, r := decide(t, s, cmd(dm, "place_token", `{"actor":"act_nope","at":{"x":3,"y":3}}`)); r == nil {
		t.Fatal("placed a token for a missing character")
	}

	// Kai controls the token through his character, not the token itself.
	if _, r := decide(t, s, cmd(kai, "move_token", `{"token":"tok_kai","to":{"x":1,"y":0}}`)); r != nil {
		t.Fatalf("Kai can't move his PC's token: %v", r)
	}
	if _, r := decide(t, s, cmd(ana, "move_token", `{"token":"tok_kai","to":{"x":1,"y":0}}`)); r == nil || r.Code != CodeForbidden {
		t.Fatalf("Ana moved Kai's token: %v", r)
	}

	// Deleting a character removes its token too.
	evs, r = decide(t, s, cmd(dm, "delete_character", `{"actor":"act_kai"}`))
	if r != nil || !reflect.DeepEqual(evs, []Payload{TokenRemoved{Token: "tok_kai"}, CharacterDeleted{Actor: kaiPC}}) {
		t.Fatalf("delete = %+v, %v", evs, r)
	}
}

// State never shares slices with the events that built it.
func TestApplyCharacterCopies(t *testing.T) {
	s := NewState("ses_1")
	ev := CharacterCreated{Character: Character{ID: "a", Name: "A", Conditions: []Condition{{Name: "Prone"}}, Controllers: []UserID{kai}}}
	s.Apply(Event{Seq: 1, Data: ev})
	s.Apply(Event{Seq: 2, Data: ConditionRemoved{Actor: "a", Name: "Prone"}})
	s.Actors["a"].Controllers[0] = ana
	if len(ev.Character.Conditions) != 1 || ev.Character.Controllers[0] != kai {
		t.Fatalf("event changed: %+v", ev.Character)
	}
}
