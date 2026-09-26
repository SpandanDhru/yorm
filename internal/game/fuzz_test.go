package game

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
)

// fuzzState is a busy mid-combat session: fog, terrain, hidden tokens,
// characters of every kind, and a turn in progress.
func fuzzState(t *testing.T) *State {
	s := fight(t) // goblin, Kai, Ana; goblin's turn
	s.Map.Terrain[Cell{4, 4}] = TerrainWall
	s.Map.Terrain[Cell{5, 4}] = TerrainDifficult
	s.Tokens["tok_hidden"] = &Token{ID: "tok_hidden", Label: "Shade", Pos: Cell{7, 1}, Size: 2, Controllers: []UserID{}, Hidden: true}
	s.Map.Fog = Fog{Enabled: true}
	s.Map.paintFog("", []Cell{{0, 0}, {1, 0}, {9, 7}}, true)
	s.Members["usr_tv"] = &Member{UserID: "usr_tv", Role: "spectator"}
	return s
}

// FuzzDecide sends arbitrary args, as any command, from any member. Decide
// must never panic; an accepted command must leave the state sound; and
// projecting its events for players must not panic either.
func FuzzDecide(f *testing.F) {
	names := slices.Sorted(maps.Keys(deciders))
	for _, seed := range []string{
		`{"token":"tok_kai","to":{"x":3,"y":3}}`,
		`{"actor":"act_kai","delta":-5}`,
		`{"text":"2d20kh1+5 = 12 8 attack","secret":true}`,
		`{"terrain":"wall","rect":{"from":{"x":0,"y":0},"to":{"x":9,"y":7}}}`,
		`{"for":"usr_kai","cells":[{"x":1,"y":1}]}`,
		`{"kind":"bonus","dash":true}`,
		`{"actor":"act_ana","roll":20}`,
		`{"image_url":"/uploads/m.png","cols":200,"rows":200,"keep_terrain":true}`,
		`{}`, `null`, `[]`, `{"actor":null}`,
	} {
		for i := range names {
			f.Add(uint8(i), uint8(0), []byte(seed))
		}
	}
	users := []UserID{dm, kai, ana, "usr_tv", "usr_stranger"}
	f.Fuzz(func(t *testing.T, name, by uint8, args []byte) {
		s := fuzzState(t)
		c := Command{ID: "c", By: users[int(by)%len(users)], Name: names[int(name)%len(names)], Args: args}
		env := Env{NewID: testEnv.NewID, Roll: func(n int) int { return n }}
		evs, err := Decide(s, c, env)
		if err != nil {
			var r *Reject
			if !errors.As(err, &r) {
				t.Fatalf("%s: non-reject error %v", c.Name, err)
			}
			return
		}
		for _, p := range evs {
			// Through JSON, as the store would, then applied.
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			data, err := DecodePayload(p.EventName(), Version(p.EventName()), b)
			if err != nil {
				t.Fatalf("%s: its own event doesn't decode: %v", c.Name, err)
			}
			ev := Event{Seq: s.Seq + 1, Name: p.EventName(), By: c.By, Data: data}
			before := s.Visible(kaiView)
			s.Apply(ev)
			checkInvariants(t, s, ev)
			Deliver(s, kaiView, ev, before)
			s.View(Viewer{User: "usr_tv"})
		}
	})
}

// FuzzDecodePayload: whatever bytes are stored, decoding never panics.
func FuzzDecodePayload(f *testing.F) {
	for name := range payloads {
		f.Add(name, uint8(1), []byte(`{}`))
	}
	f.Add("MapSet", uint8(1), []byte(`{"map":{"terrain":{"1,2":"wall","x":"y"},"fog":{"party":"!!"}}}`))
	f.Add("TokenMoved", uint8(0), []byte(`{"token":"t"}`))
	f.Fuzz(func(t *testing.T, name string, version uint8, data []byte) {
		_, _ = DecodePayload(name, int(version), data)
	})
}
