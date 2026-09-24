package game

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
)

const (
	dm   UserID  = "usr_dm"
	kai  UserID  = "usr_kai"
	ana  UserID  = "usr_ana"
	tok1 TokenID = "tok_1"
)

var testEnv = Env{NewID: func(prefix string) string { return prefix + "_new" }}

// fixture is a 10x8 map with a medium token at (2, 3) controlled by kai.
func fixture() *State {
	s := NewState("ses_1")
	s.Members[dm] = &Member{UserID: dm, DisplayName: "DM", Role: auth.RoleDM}
	s.Members[kai] = &Member{UserID: kai, DisplayName: "Kai", Role: auth.RolePlayer}
	s.Members[ana] = &Member{UserID: ana, DisplayName: "Ana", Role: auth.RolePlayer}
	s.Map = &Map{ID: "map_1", ImageURL: "/uploads/map_1.png", Cols: 10, Rows: 8, CellFeet: 5, Terrain: Terrain{
		{5, 5}: TerrainWall, {9, 7}: TerrainDifficult,
	}}
	s.Tokens[tok1] = &Token{ID: tok1, Label: "Rogue", Color: "#aa0000", Pos: Cell{2, 3}, Size: 1, Controllers: []UserID{kai}}
	return s
}

func cmd(by UserID, name, args string) Command {
	return Command{ID: "cmd_1", By: by, Name: name, Args: json.RawMessage(args)}
}

func TestDecideRejects(t *testing.T) {
	noMap := fixture()
	noMap.Map = nil

	tests := []struct {
		name  string
		state *State
		cmd   Command
		code  string
	}{
		{"unknown command", fixture(), cmd(dm, "fly", `{}`), CodeInvalidCommand},
		{"non-member", fixture(), cmd("usr_stranger", "move_token", `{"token":"tok_1","to":{"x":0,"y":0}}`), CodeForbidden},
		{"missing args", fixture(), cmd(dm, "move_token", ``), CodeInvalidCommand},
		{"malformed args", fixture(), cmd(dm, "move_token", `{"token":7}`), CodeInvalidCommand},

		{"player sets map", fixture(), cmd(kai, "set_map", `{"image_url":"/uploads/a.png","cols":5,"rows":5}`), CodeForbidden},
		{"external image", fixture(), cmd(dm, "set_map", `{"image_url":"https://evil.example/a.png","cols":5,"rows":5}`), CodeInvalidTarget},
		{"image path traversal", fixture(), cmd(dm, "set_map", `{"image_url":"/uploads/../x.png","cols":5,"rows":5}`), CodeInvalidTarget},
		{"zero cols", fixture(), cmd(dm, "set_map", `{"image_url":"/uploads/a.png","cols":0,"rows":5}`), CodeInvalidTarget},
		{"huge grid", fixture(), cmd(dm, "set_map", `{"image_url":"/uploads/a.png","cols":201,"rows":5}`), CodeInvalidTarget},
		{"bad background", fixture(), cmd(dm, "set_map", `{"background":"beige","cols":5,"rows":5}`), CodeInvalidTarget},
		{"regrid without a map", noMap, cmd(dm, "set_map", `{"cols":5,"rows":5,"keep_terrain":true}`), CodeInvalidTarget},

		{"player paints", fixture(), cmd(kai, "paint_cells", `{"terrain":"wall","cells":[{"x":0,"y":0}]}`), CodeForbidden},
		{"paint unknown terrain", fixture(), cmd(dm, "paint_cells", `{"terrain":"lava","cells":[{"x":0,"y":0}]}`), CodeInvalidTarget},
		{"paint nothing", fixture(), cmd(dm, "paint_cells", `{"terrain":"wall"}`), CodeInvalidTarget},
		{"paint off map", fixture(), cmd(dm, "paint_cells", `{"terrain":"wall","cells":[{"x":10,"y":0}]}`), CodeInvalidTarget},
		{"paint rect off map", fixture(), cmd(dm, "paint_cells", `{"terrain":"wall","rect":{"from":{"x":0,"y":0},"to":{"x":3,"y":8}}}`), CodeInvalidTarget},
		{"paint without map", noMap, cmd(dm, "paint_cells", `{"terrain":"wall","cells":[{"x":0,"y":0}]}`), CodeInvalidTarget},

		{"player changes settings", fixture(), cmd(kai, "set_settings", `{"diagonal":"5-10-5"}`), CodeForbidden},
		{"unknown diagonal rule", fixture(), cmd(dm, "set_settings", `{"diagonal":"7"}`), CodeInvalidTarget},

		{"player places", fixture(), cmd(kai, "place_token", `{"label":"X","at":{"x":0,"y":0}}`), CodeForbidden},
		{"place without map", noMap, cmd(dm, "place_token", `{"label":"X","at":{"x":0,"y":0}}`), CodeInvalidTarget},
		{"place empty label", fixture(), cmd(dm, "place_token", `{"label":"  ","at":{"x":0,"y":0}}`), CodeInvalidTarget},
		{"place bad color", fixture(), cmd(dm, "place_token", `{"label":"X","color":"red","at":{"x":0,"y":0}}`), CodeInvalidTarget},
		{"place too big", fixture(), cmd(dm, "place_token", `{"label":"X","size":5,"at":{"x":0,"y":0}}`), CodeInvalidTarget},
		{"place off map", fixture(), cmd(dm, "place_token", `{"label":"X","at":{"x":10,"y":0}}`), CodeInvalidTarget},
		{"large overhangs edge", fixture(), cmd(dm, "place_token", `{"label":"X","size":2,"at":{"x":9,"y":0}}`), CodeInvalidTarget},
		{"unknown controller", fixture(), cmd(dm, "place_token", `{"label":"X","at":{"x":0,"y":0},"controllers":["usr_ghost"]}`), CodeInvalidTarget},

		{"move missing token", fixture(), cmd(dm, "move_token", `{"token":"tok_nope","to":{"x":0,"y":0}}`), CodeInvalidTarget},
		{"move someone else's token", fixture(), cmd(ana, "move_token", `{"token":"tok_1","to":{"x":0,"y":0}}`), CodeForbidden},
		{"move off map", fixture(), cmd(kai, "move_token", `{"token":"tok_1","to":{"x":-1,"y":0}}`), CodeInvalidTarget},
		{"move past bottom", fixture(), cmd(kai, "move_token", `{"token":"tok_1","to":{"x":0,"y":8}}`), CodeInvalidTarget},
		{"player moves into wall", fixture(), cmd(kai, "move_token", `{"token":"tok_1","to":{"x":5,"y":5}}`), CodeInvalidTarget},

		{"player removes", fixture(), cmd(kai, "remove_token", `{"token":"tok_1"}`), CodeForbidden},
		{"remove missing", fixture(), cmd(dm, "remove_token", `{"token":"tok_nope"}`), CodeInvalidTarget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := Decide(tt.state, tt.cmd, testEnv)
			var r *Reject
			if !errors.As(err, &r) {
				t.Fatalf("Decide = %v, %v; want reject %s", evs, err, tt.code)
			}
			if r.Code != tt.code {
				t.Fatalf("code = %s (%s), want %s", r.Code, r.Message, tt.code)
			}
		})
	}
}

func TestDecideAccepts(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		want []Payload
	}{
		{
			"DM sets map, cell_feet defaults to 5",
			cmd(dm, "set_map", `{"image_url":"/uploads/b.png","cols":20,"rows":15}`),
			[]Payload{MapSet{Map: Map{ID: "map_new", ImageURL: "/uploads/b.png", Background: defaultBackground, Cols: 20, Rows: 15, CellFeet: 5, Terrain: Terrain{}}}},
		},
		{
			"blank map",
			cmd(dm, "set_map", `{"background":"#FFFFFF","cols":30,"rows":20}`),
			[]Payload{MapSet{Map: Map{ID: "map_new", Background: "#FFFFFF", Cols: 30, Rows: 20, CellFeet: 5, Terrain: Terrain{}}}},
		},
		{
			"regrid keeps the map ID and the terrain that still fits",
			cmd(dm, "set_map", `{"image_url":"/uploads/map_1.png","cols":9,"rows":7,"cell_feet":10,"keep_terrain":true}`),
			[]Payload{MapSet{Map: Map{
				ID: "map_1", ImageURL: "/uploads/map_1.png", Background: defaultBackground, Cols: 9, Rows: 7, CellFeet: 10,
				Terrain: Terrain{{5, 5}: TerrainWall},
			}}},
		},
		{
			"paint cells and a rectangle",
			cmd(dm, "paint_cells", `{"terrain":"water","cells":[{"x":0,"y":0}],"rect":{"from":{"x":3,"y":2},"to":{"x":1,"y":1}}}`),
			[]Payload{CellsPainted{Terrain: TerrainWater, Cells: []Cell{{0, 0}}, Rect: &Rect{From: Cell{3, 2}, To: Cell{1, 1}}}},
		},
		{
			"erase",
			cmd(dm, "paint_cells", `{"terrain":"clear","cells":[{"x":5,"y":5}]}`),
			[]Payload{CellsPainted{Terrain: TerrainClear, Cells: []Cell{{5, 5}}}},
		},
		{
			"switch to 5-10-5",
			cmd(dm, "set_settings", `{"diagonal":"5-10-5"}`),
			[]Payload{SettingsChanged{Settings: Settings{Diagonal: DiagonalAlternating}}},
		},
		{
			"player moves onto difficult terrain",
			cmd(kai, "move_token", `{"token":"tok_1","to":{"x":9,"y":7}}`),
			[]Payload{TokenMoved{Token: tok1, From: Cell{2, 3}, To: Cell{9, 7}}},
		},
		{
			"DM can drop a token on a wall",
			cmd(dm, "move_token", `{"token":"tok_1","to":{"x":5,"y":5}}`),
			[]Payload{TokenMoved{Token: tok1, From: Cell{2, 3}, To: Cell{5, 5}}},
		},
		{
			"DM places token with defaults, duplicate controllers collapse",
			cmd(dm, "place_token", `{"label":" Goblin ","at":{"x":9,"y":7},"controllers":["usr_ana","usr_ana"]}`),
			[]Payload{TokenPlaced{Token: Token{
				ID: "tok_new", Label: "Goblin", Color: defaultColor, Pos: Cell{9, 7}, Size: 1, Controllers: []UserID{ana},
			}}},
		},
		{
			"large token fits in the corner",
			cmd(dm, "place_token", `{"label":"Ogre","size":2,"color":"#00FF00","at":{"x":8,"y":6}}`),
			[]Payload{TokenPlaced{Token: Token{
				ID: "tok_new", Label: "Ogre", Color: "#00FF00", Pos: Cell{8, 6}, Size: 2, Controllers: []UserID{},
			}}},
		},
		{
			"DM moves any token",
			cmd(dm, "move_token", `{"token":"tok_1","to":{"x":0,"y":0}}`),
			[]Payload{TokenMoved{Token: tok1, From: Cell{2, 3}, To: Cell{0, 0}}},
		},
		{
			"DM removes token",
			cmd(dm, "remove_token", `{"token":"tok_1"}`),
			[]Payload{TokenRemoved{Token: tok1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decide(fixture(), tt.cmd, testEnv)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	s := NewState("ses_1")
	evs := []Payload{
		MemberJoined{Member: Member{UserID: dm, DisplayName: "DM", Role: auth.RoleDM}},
		MapSet{Map: Map{ID: "map_1", ImageURL: "/uploads/m.png", Cols: 10, Rows: 8, CellFeet: 5}},
		TokenPlaced{Token: Token{ID: "tok_a", Label: "A", Pos: Cell{1, 1}, Size: 1}},
		TokenPlaced{Token: Token{ID: "tok_b", Label: "B", Pos: Cell{2, 2}, Size: 1}},
		TokenMoved{Token: "tok_a", From: Cell{1, 1}, To: Cell{5, 5}},
		TokenRemoved{Token: "tok_b"},
	}
	for i, p := range evs {
		s.Apply(Event{Seq: int64(i + 1), Name: p.EventName(), Data: p})
	}
	if s.Seq != 6 {
		t.Fatalf("Seq = %d, want 6", s.Seq)
	}
	if !s.IsDM(dm) || s.Map.ID != "map_1" {
		t.Fatalf("member or map not applied: %+v", s)
	}
	if len(s.Tokens) != 1 || s.Tokens["tok_a"].Pos != (Cell{5, 5}) {
		t.Fatalf("tokens = %+v", s.Tokens)
	}
}

// Apply must not keep pointers into the event, or later mutations of state
// would rewrite history held elsewhere (the broadcast copy, a test's slice).
func TestApplyCopiesEventData(t *testing.T) {
	s := fixture()
	ev := TokenPlaced{Token: Token{ID: "tok_z", Label: "Z", Pos: Cell{0, 0}, Size: 1}}
	s.Apply(Event{Seq: 1, Name: ev.EventName(), Data: ev})
	s.Apply(Event{Seq: 2, Name: "TokenMoved", Data: TokenMoved{Token: "tok_z", To: Cell{4, 4}}})
	if ev.Token.Pos != (Cell{0, 0}) {
		t.Fatal("Apply mutated the event's token")
	}
}

// Events survive the round trip through JSON, which is how they are stored.
func TestPayloadRoundTrip(t *testing.T) {
	all := []Payload{
		MemberJoined{Member: Member{UserID: kai, DisplayName: "Kai", Role: auth.RolePlayer}},
		MapSet{Map: Map{ID: "m", ImageURL: "/uploads/m.png", Background: "#000000", Cols: 3, Rows: 4, CellFeet: 5, Terrain: Terrain{{1, 2}: TerrainWall}}},
		CellsPainted{Terrain: TerrainHazard, Cells: []Cell{{0, 1}}, Rect: &Rect{From: Cell{0, 0}, To: Cell{1, 1}}},
		SettingsChanged{Settings: Settings{Diagonal: DiagonalAlternating}},
		TokenPlaced{Token: Token{ID: "t", Label: "T", Color: "#123456", Pos: Cell{1, 2}, Size: 2, Controllers: []UserID{kai}}},
		TokenMoved{Token: "t", From: Cell{1, 2}, To: Cell{3, 4}},
		TokenRemoved{Token: "t"},
		CharacterCreated{Character: Character{
			ID: "a", Kind: KindPC, Name: "Kai", Class: "Rogue", Level: 3, AC: 15, Speed: 30, InitBonus: 3,
			HP: HitPoints{Current: 20, Max: 24, Temp: 2}, Conditions: []Condition{{Name: "Prone"}}, Controllers: []UserID{kai}, RollsOwnDice: true,
		}},
		CharacterUpdated{Character: Character{ID: "a", Kind: KindMonster, Name: "Goblin", Conditions: []Condition{}, Controllers: []UserID{}}},
		CharacterDeleted{Actor: "a"},
		HPChanged{Actor: "a", HP: HitPoints{Current: 3, Max: 7}, Delta: -4},
		ConditionAdded{Actor: "a", Condition: Condition{Name: "Poisoned", Source: "Giant spider"}},
		ConditionRemoved{Actor: "a", Name: "Poisoned"},
	}
	if len(all) != len(payloads) {
		t.Fatalf("test covers %d payloads, registry has %d", len(all), len(payloads))
	}
	for _, p := range all {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodePayload(p.EventName(), b)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, p) {
			t.Errorf("%s: got %+v, want %+v", p.EventName(), got, p)
		}
	}
	if _, err := DecodePayload("Nope", []byte(`{}`)); err == nil {
		t.Error("unknown event decoded without error")
	}
}

// Replaying accepted events rebuilds exactly the state that produced them.
func TestReplayMatchesLiveState(t *testing.T) {
	n := 0
	env := Env{NewID: func(p string) string { n++; return p + "_" + strconv.Itoa(n) }}
	live := NewState("ses_1")
	var log []Event
	run := func(c Command) {
		t.Helper()
		ps, err := Decide(live, c, env)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		for _, p := range ps {
			ev := Event{Seq: live.Seq + 1, Name: p.EventName(), By: c.By, At: time.Unix(0, 0)}
			b, _ := json.Marshal(p)
			ev.Data, _ = DecodePayload(ev.Name, b)
			live.Apply(ev)
			log = append(log, ev)
		}
	}
	for _, m := range []Member{{UserID: dm, Role: auth.RoleDM}, {UserID: kai, Role: auth.RolePlayer}} {
		ev := Event{Seq: live.Seq + 1, Name: "MemberJoined", Data: MemberJoined{Member: m}}
		live.Apply(ev)
		log = append(log, ev)
	}
	run(cmd(dm, "set_map", `{"image_url":"/uploads/m.png","cols":10,"rows":10}`))
	run(cmd(dm, "paint_cells", `{"terrain":"wall","rect":{"from":{"x":4,"y":0},"to":{"x":4,"y":8}}}`))
	run(cmd(dm, "paint_cells", `{"terrain":"clear","cells":[{"x":4,"y":4}]}`))
	run(cmd(dm, "set_settings", `{"diagonal":"5-10-5"}`))
	run(cmd(dm, "set_map", `{"image_url":"/uploads/m.png","cols":8,"rows":8,"keep_terrain":true}`))
	run(cmd(dm, "place_token", `{"label":"Rogue","at":{"x":0,"y":0},"controllers":["usr_kai"]}`))
	run(cmd(dm, "place_token", `{"label":"Goblin","at":{"x":5,"y":5}}`))
	for id := range live.Tokens {
		run(cmd(dm, "move_token", `{"token":"`+string(id)+`","to":{"x":3,"y":3}}`))
	}
	run(cmd(kai, "create_character", `{"name":"Kai","max_hp":24,"speed":30}`))
	run(cmd(dm, "create_character", `{"name":"Ogre","max_hp":59}`))
	for id, c := range live.Actors {
		a := `{"actor":"` + string(id) + `"`
		run(cmd(dm, "place_token", a+`,"at":{"x":1,"y":1}}`))
		run(cmd(dm, "adjust_hp", a+`,"delta":-7}`))
		run(cmd(dm, "set_temp_hp", a+`,"temp":5}`))
		run(cmd(dm, "add_condition", a+`,"name":"Prone"}`))
		run(cmd(dm, "update_character", a+`,"ac":17}`))
		if c.Name == "Ogre" {
			run(cmd(dm, "delete_character", a+`}`))
		}
	}

	replayed := NewState("ses_1")
	for _, ev := range log {
		replayed.Apply(ev)
	}
	if !reflect.DeepEqual(replayed, live) {
		t.Fatalf("replayed state differs\nlive     %+v\nreplayed %+v", live, replayed)
	}
}

func TestApplyTerrain(t *testing.T) {
	s := NewState("ses_1")
	mapSet := MapSet{Map: Map{ID: "m", Cols: 6, Rows: 6, CellFeet: 5}}
	s.Apply(Event{Seq: 1, Data: mapSet})
	s.Apply(Event{Seq: 2, Data: CellsPainted{Terrain: TerrainWall, Rect: &Rect{From: Cell{2, 2}, To: Cell{0, 0}}}})
	s.Apply(Event{Seq: 3, Data: CellsPainted{Terrain: TerrainWater, Cells: []Cell{{5, 5}, {1, 1}}}})
	s.Apply(Event{Seq: 4, Data: CellsPainted{Terrain: TerrainClear, Cells: []Cell{{0, 0}, {4, 4}}}})

	want := Terrain{
		{1, 0}: TerrainWall, {2, 0}: TerrainWall,
		{0, 1}: TerrainWall, {1, 1}: TerrainWater, {2, 1}: TerrainWall,
		{0, 2}: TerrainWall, {1, 2}: TerrainWall, {2, 2}: TerrainWall,
		{5, 5}: TerrainWater,
	}
	if !reflect.DeepEqual(s.Map.Terrain, want) {
		t.Fatalf("terrain = %v\nwant      %v", s.Map.Terrain, want)
	}
	if mapSet.Map.Terrain != nil {
		t.Fatal("painting changed the MapSet event")
	}
}

func TestTerrainJSON(t *testing.T) {
	in := Terrain{{3, 4}: TerrainWall, {10, 0}: TerrainDifficult}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"10,0":"difficult","3,4":"wall"}` {
		t.Fatalf("JSON = %s", b)
	}
	var out Terrain
	if err := json.Unmarshal(b, &out); err != nil || !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %v, %v", out, err)
	}
	if err := json.Unmarshal([]byte(`{"x":"wall"}`), &out); err == nil {
		t.Fatal("bad key decoded without error")
	}
}

// mapOf builds a map from rows of text: '.' clear, '#' wall, '~' water,
// ':' difficult, '!' hazard.
func mapOf(rows ...string) *Map {
	m := &Map{Cols: len(rows[0]), Rows: len(rows), CellFeet: 5, Terrain: Terrain{}}
	kinds := map[rune]TerrainKind{'#': TerrainWall, '~': TerrainWater, ':': TerrainDifficult, '!': TerrainHazard}
	for y, row := range rows {
		for x, r := range row {
			if k, ok := kinds[r]; ok {
				m.Terrain[Cell{x, y}] = k
			}
		}
	}
	return m
}

func TestPathCost(t *testing.T) {
	open := mapOf(
		"......",
		"......",
		"......",
		"......",
	)
	tests := []struct {
		name     string
		m        *Map
		rule     DiagonalRule
		size     int
		from, to Cell
		limit    int
		feet     int
		ok       bool
	}{
		{"stay put", open, DiagonalFive, 1, Cell{1, 1}, Cell{1, 1}, -1, 0, true},
		{"straight line", open, DiagonalFive, 1, Cell{0, 0}, Cell{5, 0}, -1, 25, true},
		{"diagonal is 5 ft", open, DiagonalFive, 1, Cell{0, 0}, Cell{3, 3}, -1, 15, true},
		{"5-10-5: 3 diagonals = 20 ft", open, DiagonalAlternating, 1, Cell{0, 0}, Cell{3, 3}, -1, 20, true},
		{"5-10-5: 2 diagonals = 15 ft", open, DiagonalAlternating, 1, Cell{0, 0}, Cell{2, 2}, -1, 15, true},
		{"5-10-5: knight move = 10 ft", open, DiagonalAlternating, 1, Cell{0, 0}, Cell{2, 1}, -1, 10, true},
		// Down 3, around the wall's end without cutting its corner (2 steps), up 3.
		{"walk around a wall", mapOf(
			".#....",
			".#....",
			".#....",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{2, 0}, -1, 40, true},
		{"sealed off", mapOf(
			".#....",
			"##....",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{3, 3}, -1, 0, false},
		{"no squeezing diagonally past a corner", mapOf(
			".#....",
			"#.....",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{1, 1}, -1, 0, false},
		{"destination is a wall", mapOf(
			"...#..",
			"......",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{3, 0}, -1, 0, false},
		{"difficult terrain doubles", mapOf(
			".::...",
			"######",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{3, 0}, -1, 25, true},
		{"water doubles too", mapOf(
			".~....",
			"######",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{2, 0}, -1, 15, true},
		{"cheaper to go around difficult terrain", mapOf(
			"......",
			".:::..",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 1}, Cell{4, 1}, -1, 20, true},
		{"hazards cost nothing extra", mapOf(
			".!!!..",
			"......",
			"......",
			"......",
		), DiagonalFive, 1, Cell{0, 0}, Cell{4, 0}, -1, 20, true},
		{"large token fits through a 2-wide gap", mapOf(
			"......",
			"#..###",
			"#..###",
			"......",
		), DiagonalFive, 2, Cell{1, 0}, Cell{1, 2}, -1, 10, true},
		{"large token blocked by a 1-wide gap", mapOf(
			"......",
			"#.####",
			"#.####",
			"......",
		), DiagonalFive, 2, Cell{0, 0}, Cell{0, 2}, -1, 0, false},
		{"large token pays double if any of it is difficult", mapOf(
			"......",
			"...:..",
			"......",
			"......",
		), DiagonalFive, 2, Cell{0, 0}, Cell{2, 0}, -1, 15, true},
		{"reachable within limit", open, DiagonalFive, 1, Cell{0, 0}, Cell{5, 0}, 25, 25, true},
		{"beyond limit", open, DiagonalFive, 1, Cell{0, 0}, Cell{5, 0}, 20, 0, false},
		{"off the map", open, DiagonalFive, 1, Cell{0, 0}, Cell{6, 0}, -1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feet, ok := PathCost(tt.m, tt.rule, tt.size, tt.from, tt.to, tt.limit)
			if feet != tt.feet || ok != tt.ok {
				t.Fatalf("PathCost = %d, %v; want %d, %v", feet, ok, tt.feet, tt.ok)
			}
		})
	}
}

// Cell size scales costs: on a 10-foot grid every step costs 10.
func TestPathCostUsesCellFeet(t *testing.T) {
	m := mapOf("....")
	m.CellFeet = 10
	if feet, _ := PathCost(m, DiagonalFive, 1, Cell{0, 0}, Cell{3, 0}, -1); feet != 30 {
		t.Fatalf("feet = %d, want 30", feet)
	}
}
