package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/session/sessiontest"
)

// ambush sets up a map, a DM, and Kai, plus a goblin the DM has placed
// hidden. It returns the goblin's token ID.
func ambush(t *testing.T, m *Manager) (dm, kai *fakeClient, gobToken string) {
	t.Helper()
	dm, _ = table(t, m)
	kai = join(t, m, "ses_1", "usr_kai", auth.RolePlayer)
	dm.expect("event", "MemberJoined")
	kai.snapshot()

	dm.submit("g1", "create_character", `{"name":"Goblin","max_hp":7,"ac":15}`)
	created := dm.expect("event", "CharacterCreated")
	dm.expect("ack", "")
	if h := kai.next(); h.Type != "event" || h.Name != "Hidden" {
		t.Fatalf("Kai got %+v for the off-map goblin, want a placeholder", h)
	}
	var cc game.CharacterCreated
	_ = json.Unmarshal(created.Data, &cc)
	dm.submit("g2", "place_token", `{"actor":"`+string(cc.Character.ID)+`","at":{"x":3,"y":3},"hidden":true}`)
	placed := dm.expect("event", "TokenPlaced")
	dm.expect("ack", "")
	if h := kai.next(); h.Name != "Hidden" {
		t.Fatalf("Kai got %+v for the hidden goblin's token", h)
	}
	var tp game.TokenPlaced
	_ = json.Unmarshal(placed.Data, &tp)
	return dm, kai, string(tp.Token.ID)
}

func TestPlayersSeeOnlyWhatTheyMay(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore("ses_1"), DefaultOptions())
	dm, kai, gob := ambush(t, m)
	if s := kai.snapshot(); len(s.Actors) != 0 || s.Tokens[game.TokenID(gob)] != nil {
		t.Fatalf("Kai's view has the goblin: %+v", s)
	}

	// A secret roll is a placeholder for Kai, the real thing for the DM.
	dm.submit("s1", "roll_dice", `{"text":"1d20 stealth","secret":true}`)
	if r := dm.expect("event", "DiceRolled"); r.Data == nil {
		t.Fatal("the DM's secret roll has no data")
	}
	dm.expect("ack", "")
	if h := kai.next(); h.Name != "Hidden" || h.Data != nil && string(h.Data) != "{}" {
		t.Fatalf("Kai got %+v for the secret roll", h)
	}

	// The reveal reaches Kai as a fresh view with the goblin, masked.
	dm.submit("r1", "set_token_hidden", `{"token":"`+gob+`","hidden":false}`)
	dm.expect("event", "TokenRevealed")
	dm.expect("ack", "")
	fresh := kai.expect("snapshot", "")
	var gobChar *game.Character
	for _, a := range fresh.State.Actors {
		gobChar = a
	}
	if fresh.State.Tokens[game.TokenID(gob)] == nil || gobChar == nil || !gobChar.Masked || gobChar.AC != 0 || gobChar.HPState != game.HPHealthy {
		t.Fatalf("Kai's view after the reveal: tokens %v, goblin %+v", fresh.State.Tokens, gobChar)
	}

	// Damage to it then comes as a masked event.
	dm.submit("d1", "adjust_hp", `{"actor":"`+string(gobChar.ID)+`","delta":-5}`)
	dm.expect("event", "HPChanged")
	dm.expect("ack", "")
	var hp game.HPChanged
	_ = json.Unmarshal(kai.expect("event", "HPChanged").Data, &hp)
	if hp.HP != (game.HitPoints{}) || hp.HPState != game.HPBloodied || hp.Delta != -1 {
		t.Fatalf("Kai's HPChanged = %+v", hp)
	}
}

// Catch-up batches are per player, and fall back to a snapshot if what the
// player could see changed in the part they missed.
func TestPlayerCatchUp(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore("ses_1"), DefaultOptions())
	dm, kai, gob := ambush(t, m)
	seen := kai.snapshot().Seq

	dm.submit("s1", "roll_dice", `{"text":"1d20","secret":true}`)
	dm.expect("event", "DiceRolled")
	dm.expect("ack", "")
	kai.next()
	if err := kai.h.Sync(context.Background(), seen); err != nil {
		t.Fatal(err)
	}
	if got := kai.next(); got.Type != "events" || len(got.Events) != 1 || got.Events[0].Name != "Hidden" {
		t.Fatalf("catch-up = %+v", got)
	}

	dm.submit("r1", "set_token_hidden", `{"token":"`+gob+`","hidden":false}`)
	dm.expect("event", "TokenRevealed")
	dm.expect("ack", "")
	kai.next()
	if err := kai.h.Sync(context.Background(), seen); err != nil {
		t.Fatal(err)
	}
	if got := kai.next(); got.Type != "snapshot" {
		t.Fatalf("catch-up across a reveal = %s, want a snapshot", got.Type)
	}
}

func TestDMViewsAsAPlayer(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore("ses_1"), DefaultOptions())
	dm, _, gob := ambush(t, m)

	if err := dm.h.ViewAs(context.Background(), "usr_kai"); err != nil {
		t.Fatal(err)
	}
	if s := dm.expect("snapshot", "").State; s.Tokens[game.TokenID(gob)] != nil {
		t.Fatal("viewing as Kai, the DM still sees the hidden goblin")
	}
	// Commands still work as the DM; events come as Kai sees them.
	dm.submit("s1", "roll_dice", `{"text":"1d20","secret":true}`)
	if h := dm.next(); h.Name != "Hidden" {
		t.Fatalf("viewing as Kai, the DM got %+v", h)
	}
	dm.expect("ack", "")

	if err := dm.h.ViewAs(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if s := dm.expect("snapshot", "").State; s.Tokens[game.TokenID(gob)] == nil || len(s.SecretRolls) != 1 {
		t.Fatal("back in the DM's view, hidden things are missing")
	}
}

// Players can't use view_as to peek.
func TestPlayersCantViewAs(t *testing.T) {
	m := newManager(t, sessiontest.NewMemStore("ses_1"), DefaultOptions())
	_, kai, _ := ambush(t, m)
	if err := kai.h.ViewAs(context.Background(), "usr_dm"); err != nil {
		t.Fatal(err)
	}
	if s := kai.snapshot(); len(s.Actors) != 0 {
		t.Fatal("a player's view_as changed what they see")
	}
}
