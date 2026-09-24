package game

import (
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
)

func TestRollDice(t *testing.T) {
	s := fixture()
	s.Members["usr_watcher"] = &Member{UserID: "usr_watcher", Role: auth.RoleSpectator}

	evs := play(t, s, rolls(t, 7, 16), kai, "roll_dice", `{"text":"2d20kh1 + 5  Longsword attack"}`)
	r := evs[0].(DiceRolled)
	if r.Expr != "2d20kh1+5" || r.Label != "Longsword attack" || r.Result.Total != 21 || r.Physical {
		t.Fatalf("server roll = %+v", r)
	}

	evs = play(t, s, testEnv, kai, "roll_dice", `{"text":"2d6+3 = 4 5 fire"}`)
	if r := evs[0].(DiceRolled); r.Result.Total != 12 || !r.Physical || r.Result.Terms[0].Faces[1] != 5 {
		t.Fatalf("physical faces = %+v", r)
	}

	evs = play(t, s, testEnv, dm, "roll_dice", `{"text":"1d20+5 = 25 stealth"}`)
	if r := evs[0].(DiceRolled); r.Result.Total != 25 || !r.Physical || r.Result.Terms != nil {
		t.Fatalf("physical total = %+v", r)
	}

	for _, text := range []string{
		"fireball",     // not an expression
		"1d20+5 = 26",  // can't total more than 25
		"1d20+5 = 5",   // or less than 6
		"2d6 = 4 7",    // a d6 can't show 7
		"2d6 = 4 5 6",  // three results for two dice
		"101d6",        // too many dice
		"1d20 = 12abc", // not a number
	} {
		refuse(t, s, testEnv, kai, "roll_dice", `{"text":"`+text+`"}`, CodeInvalidExpression)
	}
	refuse(t, s, testEnv, "usr_watcher", "roll_dice", `{"text":"1d20"}`, CodeForbidden)
}

// The state keeps only the latest rolls, with who rolled and when.
func TestRecentRolls(t *testing.T) {
	s := NewState("ses_1")
	at := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)
	for i := range MaxRolls + 5 {
		s.Apply(Event{Seq: int64(i + 1), By: kai, At: at, Data: DiceRolled{Expr: "1d20", Label: "roll"}})
	}
	if len(s.Rolls) != MaxRolls || s.Rolls[0].Seq != 6 || s.Rolls[MaxRolls-1].Seq != MaxRolls+5 {
		t.Fatalf("kept %d rolls, seqs %d..%d", len(s.Rolls), s.Rolls[0].Seq, s.Rolls[len(s.Rolls)-1].Seq)
	}
	if s.Rolls[0].By != kai || !s.Rolls[0].At.Equal(at) {
		t.Fatalf("roll = %+v", s.Rolls[0])
	}
}
