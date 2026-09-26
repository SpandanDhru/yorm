package session

import (
	"encoding/json"

	"github.com/SpandanDhru/yorm/internal/game"
)

// snapshotFormat is the shape of snapshotDoc. Bump it whenever game.State
// or snapshotDoc changes in a way old snapshots can't decode into; old
// snapshots are then skipped and the session replays from events.
const snapshotFormat = 1

// snapshotDoc is what a snapshot stores: the state, plus the answers to
// recent commands so retries stay safe across a restart.
type snapshotDoc struct {
	State    *game.State `json:"state"`
	Commands []answer    `json:"commands"`
}

type cmdKey struct {
	User game.UserID
	ID   string
}

// answer is how a command was answered: accepted with the seq it reached,
// or rejected.
type answer struct {
	User   game.UserID  `json:"user"`
	ID     string       `json:"id"`
	Seq    int64        `json:"seq,omitempty"`
	Reject *game.Reject `json:"reject,omitempty"`
}

// answers remembers the most recent answers, oldest first, so a command
// sent twice (a retry after a reconnect) is answered again, not re-applied.
type answers struct {
	max   int
	order []cmdKey
	byKey map[cmdKey]answer
}

func newAnswers(limit int) *answers {
	return &answers{max: limit, byKey: make(map[cmdKey]answer)}
}

func (a *answers) get(user game.UserID, id string) (answer, bool) {
	ans, ok := a.byKey[cmdKey{user, id}]
	return ans, ok
}

func (a *answers) put(ans answer) {
	k := cmdKey{ans.User, ans.ID}
	if _, ok := a.byKey[k]; !ok {
		a.order = append(a.order, k)
	}
	a.byKey[k] = ans
	for len(a.order) > a.max {
		delete(a.byKey, a.order[0])
		a.order = a.order[1:]
	}
}

// list returns the answers oldest first, for a snapshot.
func (a *answers) list() []answer {
	out := make([]answer, len(a.order))
	for i, k := range a.order {
		out[i] = a.byKey[k]
	}
	return out
}

// learn records the answers implied by events: every accepted command left
// its ID in the Cause of its events.
func (a *answers) learn(evs []game.Event) {
	for _, ev := range evs {
		if ev.Cause != "" {
			a.put(answer{User: ev.By, ID: ev.Cause, Seq: ev.Seq})
		}
	}
}

// recent keeps the last events in memory, so a client that missed a few
// catches up without a full snapshot.
type recent struct {
	max int
	evs []game.Event
}

func (r *recent) add(evs ...game.Event) {
	r.evs = append(r.evs, evs...)
	if over := len(r.evs) - r.max; over > 0 {
		r.evs = append([]game.Event(nil), r.evs[over:]...)
	}
}

// after returns the events with seq greater than seq, if it still has all
// of them; ok is false if some have been forgotten.
func (r *recent) after(seq, current int64) (evs []game.Event, ok bool) {
	if seq == current {
		return nil, true
	}
	if len(r.evs) == 0 || r.evs[0].Seq > seq+1 {
		return nil, false
	}
	for i, ev := range r.evs {
		if ev.Seq > seq {
			return r.evs[i:], true
		}
	}
	return nil, false
}

func encodeSnapshot(s *game.State, a *answers) []byte {
	return mustMarshal(snapshotDoc{State: s, Commands: a.list()})
}

func decodeSnapshot(data []byte, maxAnswers int) (*game.State, *answers, error) {
	var doc snapshotDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	a := newAnswers(maxAnswers)
	for _, ans := range doc.Commands {
		a.put(ans)
	}
	return doc.State, a, nil
}
