package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SpandanDhru/yorm/internal/game"
)

// Table is one session with a DM bot and player bots, set up for a fight.
type Table struct {
	Session string
	DMToken string
	DM      *Client
	Players []*Client
	all     []*Client
	wg      sync.WaitGroup
}

type seat struct {
	Session    string `json:"session"`
	User       string `json:"user"`
	Token      string `json:"token"`
	InviteCode string `json:"invite_code"`
}

func post(ctx context.Context, url, body string) (seat, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return seat{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return seat{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return seat{}, fmt.Errorf("POST %s: %s", url, resp.Status)
	}
	var s seat
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// NewTable creates a session with a DM and players, connects them all
// (they keep running until ctx ends), and sets up a fight: a map, a PC per
// player, two monsters, tokens for everyone, and combat under way.
func NewTable(ctx context.Context, baseURL string, players int, stats *Stats) (*Table, error) {
	dm, err := post(ctx, baseURL+"/api/sessions", `{"name":"Load test","display_name":"DM"}`)
	if err != nil {
		return nil, err
	}
	t := &Table{Session: dm.Session, DMToken: dm.Token}
	t.DM = NewClient(baseURL, dm.Session, dm.Token, game.UserID(dm.User), true, stats)
	t.all = append(t.all, t.DM)
	for i := range players {
		p, err := post(ctx, baseURL+"/api/sessions/"+dm.Session+"/join",
			fmt.Sprintf(`{"code":%q,"display_name":"Player %d"}`, dm.InviteCode, i+1))
		if err != nil {
			return nil, err
		}
		c := NewClient(baseURL, dm.Session, p.Token, game.UserID(p.User), false, stats)
		t.Players = append(t.Players, c)
		t.all = append(t.all, c)
	}
	for _, c := range t.all {
		t.wg.Add(1)
		go func() { defer t.wg.Done(); c.Run(ctx) }()
	}
	if err := t.waitReady(ctx); err != nil {
		return nil, err
	}

	do := func(c *Client, name string, args any) error {
		a, err := c.Do(ctx, name, args)
		if err == nil && a.Code != "" {
			err = fmt.Errorf("%s rejected: %s", name, a.Code)
		}
		return err
	}
	if err := do(t.DM, "set_map", map[string]any{"cols": 20, "rows": 15}); err != nil {
		return nil, err
	}
	for i, p := range t.Players {
		if err := do(p, "create_character", map[string]any{"name": fmt.Sprintf("Hero %d", i+1), "max_hp": 30, "speed": 30, "init_bonus": i}); err != nil {
			return nil, err
		}
	}
	for i := range 2 {
		if err := do(t.DM, "create_character", map[string]any{"name": fmt.Sprintf("Goblin %d", i+1), "max_hp": 12, "ac": 15}); err != nil {
			return nil, err
		}
	}
	s := t.DM.State()
	i := 0
	for _, id := range sortedActors(s) {
		if err := do(t.DM, "place_token", map[string]any{"actor": id, "at": map[string]int{"x": 2 + 2*(i%8), "y": 2 + 2*(i/8)}}); err != nil {
			return nil, err
		}
		i++
	}
	if err := do(t.DM, "start_combat", map[string]any{}); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Table) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for _, c := range t.all {
		for !c.Ready() {
			if time.Now().After(deadline) {
				return fmt.Errorf("bot %s never connected", c.User)
			}
			select {
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

// Play has every bot act every interval (with jitter) until ctx ends:
// on their turn they move, act, and end the turn; otherwise they roll
// dice, and the DM also deals damage and heals.
func (t *Table) Play(ctx context.Context, interval time.Duration) {
	var wg sync.WaitGroup
	for _, c := range t.all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // bots' play, not a secret
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(interval/2 + time.Duration(r.Int64N(int64(interval)))):
				}
				if c.Ready() {
					act(c, r)
				}
			}
		}()
	}
	wg.Wait()
}

func act(c *Client, r *rand.Rand) {
	s := c.State()
	e := s.Encounter
	var active *game.Character
	if e != nil {
		active = s.Actors[e.Active]
	}
	mine := active != nil && (c.DM && active.Kind != game.KindPC || !c.DM && slices.Contains(active.Controllers, c.User))
	switch {
	case e == nil && c.DM:
		c.Send("start_combat", map[string]any{})
	case mine:
		t := s.TokenFor(e.Active)
		switch n := r.IntN(10); {
		case t != nil && n < 5:
			c.Send("move_token", map[string]any{"token": t.ID, "to": map[string]int{
				"x": clamp(t.Pos.X+r.IntN(5)-2, 0, s.Map.Cols-1), "y": clamp(t.Pos.Y+r.IntN(5)-2, 0, s.Map.Rows-1),
			}})
		case n < 7:
			c.Send("use_action", map[string]any{"kind": []string{"action", "bonus"}[r.IntN(2)], "dash": r.IntN(3) == 0})
		default:
			c.Send("end_turn", map[string]any{})
		}
	case c.DM && r.IntN(3) == 0:
		ids := sortedActors(s)
		c.Send("adjust_hp", map[string]any{"actor": ids[r.IntN(len(ids))], "delta": r.IntN(13) - 8})
	default:
		c.Send("roll_dice", map[string]any{"text": []string{"1d20+5 attack", "2d6+3 damage", "2d20kh1+2", "1d20 = 14 stealth"}[r.IntN(4)], "secret": c.DM && r.IntN(4) == 0})
	}
}

// Clients returns every bot at the table.
func (t *Table) Clients() []*Client { return t.all }

// Wait waits for the bots to stop, after their context ends.
func (t *Table) Wait() { t.wg.Wait() }

func sortedActors(s *game.State) []game.ActorID {
	return slices.Sorted(maps.Keys(s.Actors))
}

func clamp(n, lo, hi int) int { return max(lo, min(hi, n)) }

// Settle waits until every bot's commands are answered and the session
// has been quiet for a moment, so states can be compared. It gives up when
// ctx ends.
func Settle(ctx context.Context, clients []*Client) error {
	for _, c := range clients {
		for c.Unanswered() > 0 || !c.Ready() {
			select {
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
				return fmt.Errorf("bot %s still has %d unanswered commands", c.User, c.Unanswered())
			}
		}
	}
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
	}
	return ctx.Err()
}

// Diverged counts bots whose state, built from what they were sent, differs
// from the server's current view for them.
func Diverged(ctx context.Context, clients []*Client) (int, error) {
	n := 0
	for _, c := range clients {
		local, server, err := c.Verify(ctx)
		if err != nil {
			return n, err
		}
		if local != server {
			n++
		}
	}
	return n, nil
}
