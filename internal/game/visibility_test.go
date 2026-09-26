package game

import (
	"reflect"
	"testing"

	"github.com/SpandanDhru/yorm/internal/auth"
)

var (
	kaiView = Viewer{User: kai}
	anaView = Viewer{User: ana}
	dmView  = Viewer{User: dm, DM: true}
)

// party is a 10x8 map with Kai's PC and token at (0,0), a goblin at (5,5),
// and an orc with no token yet.
func party() *State {
	s := withCharacters() // Kai's PC with tok_kai, goblin (no token), tok_1 (Kai's plain token)
	s.Map.Terrain = Terrain{}
	s.Tokens["tok_gob"] = &Token{ID: "tok_gob", Actor: goblin, Label: "Goblin", Pos: Cell{5, 5}, Size: 1, Controllers: []UserID{}}
	s.Actors["act_orc"] = &Character{ID: "act_orc", Kind: KindMonster, Name: "Orc", AC: 13, HP: HitPoints{Current: 15, Max: 15}, Conditions: []Condition{}, Controllers: []UserID{}}
	return s
}

func apply(s *State, p Payload) Event {
	ev := Event{Seq: s.Seq + 1, Name: p.EventName(), By: dm, Data: p}
	s.Apply(ev)
	return ev
}

func TestMonstersAreMaskedAndOffMapOnesUnseen(t *testing.T) {
	s := party()
	v := s.View(kaiView)
	gob := v.Actors[goblin]
	if gob == nil || !gob.Masked || gob.HPState != HPHealthy || gob.AC != 0 || gob.HP != (HitPoints{}) || gob.Name != "Goblin" {
		t.Fatalf("goblin as Kai sees it = %+v", gob)
	}
	if v.Actors["act_orc"] != nil {
		t.Fatal("Kai sees the orc, which has no token on the map")
	}
	if pc := v.Actors[kaiPC]; pc.Masked || pc.HP.Max != 24 {
		t.Fatalf("Kai's own PC = %+v", pc)
	}
	if dmv := s.View(dmView); dmv.Actors[goblin].Masked || dmv.Actors["act_orc"] == nil {
		t.Fatal("the DM's view is masked")
	}

	// Damage to the goblin reaches Kai as a direction and a state, not numbers.
	ev := apply(s, HPChanged{Actor: goblin, HP: HitPoints{Current: 3, Max: 7}, Delta: -4})
	got := Project(s, kaiView, ev).Data
	if want := (HPChanged{Actor: goblin, Delta: -1, HPState: HPBloodied}); got != want {
		t.Fatalf("projected = %+v, want %+v", got, want)
	}
	if Project(s, dmView, ev).Data != ev.Data {
		t.Fatal("the DM's event was changed")
	}
	// Anything about the unseen orc is a placeholder.
	ev = apply(s, HPChanged{Actor: "act_orc", HP: HitPoints{Current: 5, Max: 15}, Delta: -10})
	if p := Project(s, kaiView, ev); p.Name != "Hidden" || p.Seq != ev.Seq || p.By != "" {
		t.Fatalf("projected = %+v, want a placeholder", p)
	}
}

func TestHiddenTokens(t *testing.T) {
	s := party()
	apply(s, TokenHidden{Token: "tok_gob"})
	if s.View(kaiView).Tokens["tok_gob"] != nil || s.View(kaiView).Actors[goblin] != nil {
		t.Fatal("Kai sees the hidden goblin")
	}
	if !s.View(dmView).Tokens["tok_gob"].Hidden {
		t.Fatal("the DM doesn't see the goblin marked hidden")
	}
	ev := apply(s, TokenMoved{Token: "tok_gob", From: Cell{5, 5}, To: Cell{6, 6}})
	if Project(s, kaiView, ev).Name != "Hidden" {
		t.Fatal("Kai sees the hidden goblin move")
	}
	// His own token stays visible to him even if the DM hides it.
	apply(s, TokenHidden{Token: "tok_kai"})
	if s.View(kaiView).Tokens["tok_kai"] == nil || s.View(anaView).Tokens["tok_kai"] != nil {
		t.Fatal("a hidden token should be seen by its controller only")
	}
	before := s.Visible(anaView)
	apply(s, TokenRevealed{Token: "tok_gob"})
	if s.Visible(anaView).Equal(before) {
		t.Fatal("revealing a token doesn't change what Ana sees, so she'd miss it")
	}
}

func TestFog(t *testing.T) {
	s := party()
	apply(s, FogSet{Enabled: true})
	if s.View(kaiView).Tokens["tok_gob"] != nil {
		t.Fatal("the goblin is visible in fog")
	}
	if s.View(kaiView).Tokens["tok_kai"] == nil {
		t.Fatal("Kai can't see his own token in fog")
	}
	// Revealed to Kai alone: he sees the goblin, Ana doesn't, and Ana isn't
	// even told about Kai's reveal.
	ev := apply(s, FogRevealed{For: kai, Rect: &Rect{From: Cell{4, 4}, To: Cell{6, 6}}})
	if s.View(kaiView).Tokens["tok_gob"] == nil || s.View(anaView).Tokens["tok_gob"] != nil {
		t.Fatal("a personal reveal should show the goblin to Kai only")
	}
	if Project(s, anaView, ev).Name != "Hidden" {
		t.Fatal("Ana learned of Kai's reveal")
	}
	if _, ok := s.View(anaView).Map.Fog.Users[kai]; ok {
		t.Fatal("Ana's view carries Kai's fog")
	}
	// A large token is seen if any of its cells is revealed.
	s.Tokens["tok_ogre"] = &Token{ID: "tok_ogre", Label: "Ogre", Pos: Cell{0, 5}, Size: 2, Controllers: []UserID{}}
	apply(s, FogRevealed{Cells: []Cell{{1, 6}}})
	if s.View(anaView).Tokens["tok_ogre"] == nil {
		t.Fatal("Ana can't see an ogre whose corner is revealed")
	}
	apply(s, FogSet{Enabled: false})
	if s.View(anaView).Tokens["tok_gob"] == nil {
		t.Fatal("turning fog off didn't show the goblin")
	}
}

func TestFogCommands(t *testing.T) {
	s := party()
	refuse(t, s, testEnv, kai, "set_fog", `{"enabled":true}`, CodeForbidden)
	refuse(t, s, testEnv, dm, "set_fog", `{"enabled":false}`, CodeInvalidTarget)
	play(t, s, testEnv, dm, "set_fog", `{"enabled":true}`)
	refuse(t, s, testEnv, dm, "reveal_fog", `{"for":"usr_dm","cells":[{"x":0,"y":0}]}`, CodeInvalidTarget)
	refuse(t, s, testEnv, dm, "reveal_fog", `{"cells":[{"x":10,"y":0}]}`, CodeInvalidTarget)
	refuse(t, s, testEnv, dm, "reveal_fog", `{}`, CodeInvalidTarget)
	play(t, s, testEnv, dm, "reveal_fog", `{"rect":{"from":{"x":0,"y":0},"to":{"x":2,"y":1}}}`)
	play(t, s, testEnv, dm, "hide_fog", `{"cells":[{"x":1,"y":1}]}`)
	for _, tt := range []struct {
		c    Cell
		want bool
	}{{Cell{0, 0}, true}, {Cell{2, 1}, true}, {Cell{1, 1}, false}, {Cell{3, 0}, false}} {
		if got := s.Map.Revealed(ana, tt.c); got != tt.want {
			t.Errorf("revealed %v = %v, want %v", tt.c, got, tt.want)
		}
	}
	// Changing the grid keeps the fog where the cells still exist.
	play(t, s, testEnv, dm, "set_map", `{"image_url":"/uploads/map_1.png","cols":3,"rows":3,"keep_terrain":true}`)
	if !s.Map.Fog.Enabled || !s.Map.Revealed(ana, Cell{2, 1}) || s.Map.Revealed(ana, Cell{1, 1}) {
		t.Fatalf("fog after regrid = %+v", s.Map.Fog)
	}
}

func TestHiddenCombatant(t *testing.T) {
	s := party()
	apply(s, TokenHidden{Token: "tok_gob"})
	apply(s, CombatStarted{Order: []InitEntry{
		{Actor: goblin, Total: ptr(20), Bonus: 2}, {Actor: kaiPC, Total: ptr(10), Bonus: 3},
	}})
	ev := apply(s, TurnStarted{Actor: goblin, Round: 1, Movement: 30})
	if got := Project(s, kaiView, ev).Data; got != (TurnStarted{Actor: HiddenActor, Round: 1}) {
		t.Fatalf("Kai sees the goblin's turn as %+v", got)
	}
	e := s.View(kaiView).Encounter
	if len(e.Order) != 1 || e.Active != HiddenActor || e.Economy != (TurnEconomy{Action: true, Bonus: true}) {
		t.Fatalf("Kai's encounter = %+v", e)
	}
	// Revealed mid-fight, its turn shows, but not its speed.
	apply(s, TokenRevealed{Token: "tok_gob"})
	if e := s.View(kaiView).Encounter; e.Active != goblin || e.Economy.MovementLeft != 0 || len(e.Order) != 2 {
		t.Fatalf("after reveal, Kai's encounter = %+v", e)
	}
}

func TestSecretRolls(t *testing.T) {
	s := party()
	refuse(t, s, testEnv, kai, "roll_dice", `{"text":"1d20","secret":true}`, CodeForbidden)
	evs := play(t, s, rolls(t, 17), dm, "roll_dice", `{"text":"1d20 insight","secret":true}`)
	if len(s.SecretRolls) != 1 || len(s.Rolls) != 0 || !evs[0].(DiceRolled).Secret {
		t.Fatalf("rolls %d, secret %d", len(s.Rolls), len(s.SecretRolls))
	}
	ev := Event{Seq: s.Seq, Name: "DiceRolled", Data: evs[0]}
	if Project(s, kaiView, ev).Name != "Hidden" || len(s.View(kaiView).SecretRolls) != 0 {
		t.Fatal("Kai sees the DM's secret roll")
	}
	if len(s.View(dmView).SecretRolls) != 1 {
		t.Fatal("the DM can't see their own secret roll")
	}
}

func TestHideTokenCommand(t *testing.T) {
	s := party()
	refuse(t, s, testEnv, kai, "set_token_hidden", `{"token":"tok_gob","hidden":true}`, CodeForbidden)
	refuse(t, s, testEnv, dm, "set_token_hidden", `{"token":"tok_nope","hidden":true}`, CodeInvalidTarget)
	refuse(t, s, testEnv, dm, "set_token_hidden", `{"token":"tok_gob","hidden":false}`, CodeInvalidTarget)
	if evs := play(t, s, testEnv, dm, "set_token_hidden", `{"token":"tok_gob","hidden":true}`); !reflect.DeepEqual(evs, []Payload{TokenHidden{Token: "tok_gob"}}) {
		t.Fatalf("events = %+v", evs)
	}
	evs := play(t, s, testEnv, dm, "place_token", `{"actor":"act_orc","at":{"x":2,"y":2},"hidden":true}`)
	if !evs[0].(TokenPlaced).Token.Hidden {
		t.Fatal("placed the orc visible")
	}
}

// Spectators see what players see, and control nothing.
func TestSpectatorView(t *testing.T) {
	s := party()
	s.Members["usr_tv"] = &Member{UserID: "usr_tv", Role: auth.RoleSpectator}
	v := s.View(s.ViewerFor("usr_tv"))
	if !v.Actors[goblin].Masked || v.Actors["act_orc"] != nil {
		t.Fatal("the spectator sees hidden details")
	}
}
