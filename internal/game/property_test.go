package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
)

// TestRandomPlay plays random commands from random users and checks, after
// every event, that the state is sound; and at the end, that replaying the
// log from nothing or from any snapshot rebuilds exactly the live state.
// Snapshots go through JSON, as they do in the store.
func TestRandomPlay(t *testing.T) {
	seeds, steps := 40, 400
	if testing.Short() {
		seeds = 5
	}
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 1)) //nolint:gosec // seeded on purpose, so a failure can be replayed
			n := 0
			env := Env{
				NewID: func(p string) string { n++; return fmt.Sprintf("%s_%d", p, n) },
				Roll:  func(sides int) int { return r.IntN(sides) + 1 },
			}
			live := NewState("ses")
			var log []Event
			snapshots := map[int][]byte{} // index into log -> state after it, as JSON
			// Each non-DM viewer's copy of the state, built only from what
			// they are sent.
			viewers := []Viewer{{User: kai}, {User: ana}, {User: "usr_watch"}}
			clients := map[UserID]*State{}
			stats := map[string]int{} // for kai: how each event reached him
			record := func(by UserID, cause string, p Payload) {
				// Events go through JSON, as they do in the store.
				b, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				data, err := DecodePayload(p.EventName(), Version(p.EventName()), b)
				if err != nil {
					t.Fatal(err)
				}
				ev := Event{Seq: live.Seq + 1, Name: p.EventName(), By: by, Cause: cause, At: time.Unix(int64(len(log)), 0).UTC(), Data: data}
				before := map[UserID]Visible{}
				for _, v := range viewers {
					before[v.User] = live.Visible(v)
				}
				live.Apply(ev)
				log = append(log, ev)
				checkInvariants(t, live, ev)
				for _, v := range viewers {
					c := clients[v.User]
					pev, ok := Deliver(live, v, ev, before[v.User])
					if c == nil || !ok {
						clients[v.User] = live.View(v)
						if v.User == kai {
							stats["fresh view"]++
						}
						continue
					}
					if v.User == kai {
						switch {
						case pev.Name == "Hidden":
							stats["hidden"]++
						case !reflect.DeepEqual(pev.Data, ev.Data):
							stats["redacted"]++
						default:
							stats["as is"]++
						}
					}
					c.Apply(pev)
					if want := live.View(v); !reflect.DeepEqual(c, want) {
						t.Fatalf("after %s (seq %d), %s's state built from events differs from their view: %s", ev.Name, ev.Seq, v.User, jsonDiff(c, want))
					}
					checkNoLeaks(t, live, v, c, ev)
				}
			}
			for _, m := range []Member{
				{UserID: dm, Role: auth.RoleDM}, {UserID: kai, Role: auth.RolePlayer},
				{UserID: ana, Role: auth.RolePlayer}, {UserID: "usr_watch", Role: auth.RoleSpectator},
			} {
				record(m.UserID, "", MemberJoined{Member: m})
			}

			accepted := 0
			for step := range steps {
				c := randomCommand(r, live)
				c.ID = fmt.Sprint("c", step)
				evs, err := Decide(live, c, env)
				if err != nil {
					var rej *Reject
					if !errors.As(err, &rej) {
						t.Fatalf("%s: non-reject error %v", c.Name, err)
					}
					continue
				}
				accepted++
				for _, p := range evs {
					record(c.By, c.ID, p)
				}
				if r.IntN(25) == 0 {
					b, err := json.Marshal(live)
					if err != nil {
						t.Fatal(err)
					}
					snapshots[len(log)] = b
				}
			}
			if accepted < steps/4 {
				t.Fatalf("only %d of %d random commands were accepted; the generator needs work", accepted, steps)
			}

			if testing.Verbose() {
				seen := map[string]int{}
				for _, ev := range log {
					seen[ev.Name]++
				}
				t.Logf("accepted %d/%d; events %v; kai got %v", accepted, steps, seen, stats)
			}
			replayed := NewState("ses")
			for _, ev := range log {
				replayed.Apply(ev)
			}
			if !reflect.DeepEqual(replayed, live) {
				t.Fatalf("full replay differs from live state")
			}
			for at, b := range snapshots {
				var s State
				if err := json.Unmarshal(b, &s); err != nil {
					t.Fatal(err)
				}
				for _, ev := range log[at:] {
					s.Apply(ev)
				}
				if !reflect.DeepEqual(&s, live) {
					t.Fatalf("replay from the snapshot after event %d differs from live state:\nlive     %+v\nreplayed %+v", at, live, &s)
				}
			}
		})
	}
}

// jsonDiff shows where two values' JSON first differs.
func jsonDiff(got, want any) string {
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-120)
	return fmt.Sprintf("\nbuilt ...%s\nview  ...%s", a[from:min(len(a), i+120)], b[from:min(len(b), i+120)])
}

// checkNoLeaks checks that viewer v's copy c holds nothing v may not see.
func checkNoLeaks(t *testing.T, live *State, v Viewer, c *State, ev Event) {
	t.Helper()
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("after %s (seq %d), %s can see %s", ev.Name, ev.Seq, v.User, fmt.Sprintf(format, a...))
	}
	for id, tk := range c.Tokens {
		if real := live.Tokens[id]; real.Hidden && !live.controlsToken(v.User, real) {
			fail("hidden token %s", id)
		}
		if live.Map != nil && live.Map.Fog.Enabled && !live.controlsToken(v.User, tk) {
			seen := false
			for dy := range tk.Size {
				for dx := range tk.Size {
					if cell := (Cell{tk.Pos.X + dx, tk.Pos.Y + dy}); live.Map.InBounds(cell, 1) && live.Map.Revealed(v.User, cell) {
						seen = true
					}
				}
			}
			if !seen {
				fail("token %s in fog", id)
			}
		}
	}
	for id, a := range c.Actors {
		if a.Kind != KindPC && (!a.Masked || a.HP != (HitPoints{}) || a.AC != 0 || a.Speed != 0) {
			fail("%s's stats: %+v", id, a)
		}
		if a.Kind != KindPC && live.TokenFor(id) == nil && !slices.Contains(live.Actors[id].Controllers, v.User) {
			fail("off-map monster %s", id)
		}
	}
	if len(c.SecretRolls) > 0 {
		fail("secret rolls")
	}
	if c.Map != nil {
		for u := range c.Map.Fog.Users {
			if u != v.User {
				fail("%s's fog", u)
			}
		}
	}
	if e := c.Encounter; e != nil {
		for _, x := range e.Order {
			if c.Actors[x.Actor] == nil {
				fail("hidden combatant %s in initiative", x.Actor)
			}
		}
	}
}

func checkInvariants(t *testing.T, s *State, ev Event) {
	t.Helper()
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("after %s (seq %d): %s", ev.Name, ev.Seq, fmt.Sprintf(format, a...))
	}
	for _, a := range s.Actors {
		if hp := a.HP; hp.Current < 0 || hp.Current > hp.Max || hp.Temp < 0 {
			fail("%s has HP %+v", a.Name, hp)
		}
	}
	perActor := map[ActorID]int{}
	for _, tk := range s.Tokens {
		if tk.Actor == "" {
			continue
		}
		if s.Actors[tk.Actor] == nil {
			fail("token %s stands for missing character %s", tk.ID, tk.Actor)
		}
		if perActor[tk.Actor]++; perActor[tk.Actor] > 1 {
			fail("character %s has two tokens", tk.Actor)
		}
	}
	if e := s.Encounter; e != nil {
		for _, x := range e.Order {
			if s.Actors[x.Actor] == nil {
				fail("initiative has missing character %s", x.Actor)
			}
		}
		sorted := slices.Clone(e.Order)
		(&Encounter{Order: sorted}).sortOrder()
		if !reflect.DeepEqual(sorted, e.Order) {
			fail("initiative is out of order")
		}
		if e.Active != "" {
			if e.index(e.Active) < 0 {
				fail("the active character %s is not in the fight", e.Active)
			}
			if len(e.waiting()) > 0 {
				fail("turns began while waiting for rolls")
			}
			if e.Economy.MovementLeft < 0 || e.Round < 1 {
				fail("economy %+v, round %d", e.Economy, e.Round)
			}
		}
	}
	if len(s.Rolls) > MaxRolls {
		fail("%d rolls kept", len(s.Rolls))
	}
}

// randomCommand makes a plausible command, mostly aimed at things that
// exist, so a good share is accepted.
func randomCommand(r *rand.Rand, s *State) Command {
	users := []UserID{dm, dm, kai, ana}
	by := users[r.IntN(len(users))]
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	cell := func() string {
		w, h := 12, 10
		if s.Map != nil {
			w, h = s.Map.Cols, s.Map.Rows
		}
		return fmt.Sprintf(`{"x":%d,"y":%d}`, r.IntN(w+1), r.IntN(h)) // x can be one past the edge
	}
	actor := func() string {
		ids := slices.Sorted(maps.Keys(s.Actors))
		if len(ids) == 0 || r.IntN(10) == 0 {
			return "act_missing"
		}
		return string(ids[r.IntN(len(ids))])
	}
	token := func() string {
		ids := slices.Sorted(maps.Keys(s.Tokens))
		if len(ids) == 0 || r.IntN(10) == 0 {
			return "tok_missing"
		}
		return string(ids[r.IntN(len(ids))])
	}
	c := func(name, args string) Command { return Command{By: by, Name: name, Args: json.RawMessage(args)} }

	if s.Map == nil {
		return c("set_map", fmt.Sprintf(`{"cols":%d,"rows":%d}`, 6+r.IntN(8), 6+r.IntN(6)))
	}
	// In a fight, act for the active character half the time, as its
	// controller would.
	if e := s.Encounter; e.Started() && r.IntN(2) == 0 {
		a := s.Actors[e.Active]
		if len(a.Controllers) > 0 {
			by = a.Controllers[0]
		} else {
			by = dm
		}
		switch t := s.TokenFor(a.ID); {
		case t != nil && r.IntN(3) > 0:
			return c("move_token", fmt.Sprintf(`{"token":%q,"to":{"x":%d,"y":%d}}`, t.ID, t.Pos.X+r.IntN(7)-3, t.Pos.Y+r.IntN(7)-3))
		case r.IntN(2) == 0:
			return c("use_action", fmt.Sprintf(`{"kind":%q,"dash":%v}`, pick("action", "bonus"), r.IntN(2) == 0))
		default:
			return c("end_turn", `{}`)
		}
	}
	switch r.IntN(30) {
	case 24:
		return c("set_token_hidden", fmt.Sprintf(`{"token":%q,"hidden":%v}`, token(), r.IntN(2) == 0))
	case 25:
		return c("set_fog", fmt.Sprintf(`{"enabled":%v}`, r.IntN(3) > 0))
	case 26, 27:
		return c(pick("reveal_fog", "reveal_fog", "hide_fog"), fmt.Sprintf(`{"for":%q,"rect":{"from":%s,"to":%s}}`, pick("", "", string(kai), string(ana)), cell(), cell()))
	case 28:
		return c("roll_dice", fmt.Sprintf(`{"text":"1d20+2","secret":%v}`, r.IntN(2) == 0))
	case 29:
		return c("place_token", fmt.Sprintf(`{"actor":%q,"at":%s,"hidden":true}`, actor(), cell()))
	case 0:
		return c("set_map", fmt.Sprintf(`{"cols":%d,"rows":%d,"keep_terrain":%v}`, 6+r.IntN(8), 6+r.IntN(6), r.IntN(3) > 0))
	case 1:
		return c("paint_cells", fmt.Sprintf(`{"terrain":%q,"cells":[%s,%s]}`, pick("wall", "difficult", "water", "hazard", "clear"), cell(), cell()))
	case 2:
		return c("paint_cells", fmt.Sprintf(`{"terrain":%q,"rect":{"from":%s,"to":%s}}`, pick("wall", "difficult", "clear"), cell(), cell()))
	case 3:
		return c("set_settings", fmt.Sprintf(`{"diagonal":%q}`, pick("5", "5-10-5")))
	case 4:
		return c("place_token", fmt.Sprintf(`{"label":"T","at":%s,"size":%d}`, cell(), 1+r.IntN(2)))
	case 5, 6:
		return c("place_token", fmt.Sprintf(`{"actor":%q,"at":%s}`, actor(), cell()))
	case 7, 8, 9:
		return c("move_token", fmt.Sprintf(`{"token":%q,"to":%s}`, token(), cell()))
	case 10:
		return c("remove_token", fmt.Sprintf(`{"token":%q}`, token()))
	case 11:
		return c("create_character", fmt.Sprintf(`{"name":"C%d","max_hp":%d,"speed":%d,"init_bonus":%d,"rolls_own_dice":%v}`,
			r.IntN(100), 1+r.IntN(40), 5*r.IntN(9), r.IntN(11)-3, r.IntN(3) == 0))
	case 12:
		return c("update_character", fmt.Sprintf(`{"actor":%q,"max_hp":%d,"speed":%d}`, actor(), 1+r.IntN(40), 5*r.IntN(9)))
	case 13:
		return c("adjust_hp", fmt.Sprintf(`{"actor":%q,"delta":%d}`, actor(), r.IntN(61)-40))
	case 14:
		return c("set_temp_hp", fmt.Sprintf(`{"actor":%q,"temp":%d}`, actor(), r.IntN(10)))
	case 15:
		return c(pick("add_condition", "remove_condition"), fmt.Sprintf(`{"actor":%q,"name":%q}`, actor(), pick("Prone", "Poisoned")))
	case 16:
		return c(pick("start_combat", "begin_combat", "end_combat"), `{}`)
	case 17:
		return c("set_initiative", fmt.Sprintf(`{"actor":%q,"roll":%d}`, actor(), 1+r.IntN(20)))
	case 18, 19:
		return c(pick("end_turn", "end_turn", "prev_turn"), `{}`)
	case 20:
		return c("use_action", fmt.Sprintf(`{"actor":%q,"kind":%q,"used":%v,"dash":%v}`, actor(), pick("action", "bonus", "reaction"), r.IntN(4) > 0, r.IntN(2) == 0))
	case 21:
		return c(pick("join_combat", "remove_from_combat", "delete_character"), fmt.Sprintf(`{"actor":%q}`, actor()))
	default:
		return c("roll_dice", fmt.Sprintf(`{"text":%q}`, pick("1d20+5 attack", "2d6+3 = 4 5", "2d20kh1", "1d20 = 14", "8d6")))
	}
}
