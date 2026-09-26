// Package sessiontest provides an in-memory session store for tests.
package sessiontest

import (
	"context"
	"sync"

	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/store"
)

// MemStore keeps sessions in memory with the same append contract as
// store.Postgres: an append that does not follow the last seq conflicts.
type MemStore struct {
	mu        sync.Mutex
	events    map[string][]game.Event
	members   map[string]map[game.UserID]game.Member
	snapshots map[string][]Snapshot
	appendErr error
	loads     int
}

// Snapshot is a saved snapshot.
type Snapshot struct {
	Seq    int64
	Format int
	State  []byte
}

// NewMemStore returns a store holding the given empty sessions.
func NewMemStore(sessions ...string) *MemStore {
	s := &MemStore{events: map[string][]game.Event{}, members: map[string]map[game.UserID]game.Member{}, snapshots: map[string][]Snapshot{}}
	for _, id := range sessions {
		s.events[id] = nil
		s.members[id] = map[game.UserID]game.Member{}
	}
	return s
}

// LoadAfter returns the events after seq.
func (s *MemStore) LoadAfter(_ context.Context, id string, after int64) ([]game.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, ok := s.events[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	var out []game.Event
	for _, ev := range evs {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *MemStore) SaveSnapshot(_ context.Context, id string, seq int64, format int, state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[id] = append(s.snapshots[id], Snapshot{Seq: seq, Format: format, State: state})
	return nil
}

func (s *MemStore) LatestSnapshot(_ context.Context, id string, format int) (int64, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[id]; ok {
		s.loads++ // a session start looks for a snapshot exactly once
	}
	var best *Snapshot
	for i, snap := range s.snapshots[id] {
		if snap.Format == format && (best == nil || snap.Seq > best.Seq) {
			best = &s.snapshots[id][i]
		}
	}
	if best == nil {
		return 0, nil, store.ErrNotFound
	}
	return best.Seq, best.State, nil
}

// Snapshots returns the snapshots saved for a session, oldest first.
func (s *MemStore) Snapshots(id string) []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Snapshot(nil), s.snapshots[id]...)
}

func (s *MemStore) Append(_ context.Context, id string, evs []game.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendErr; err != nil {
		s.appendErr = nil
		return err
	}
	if int64(len(s.events[id]))+1 != evs[0].Seq {
		return store.ErrConflict
	}
	s.events[id] = append(s.events[id], evs...)
	return nil
}

func (s *MemStore) Member(_ context.Context, id string, user game.UserID) (game.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[id][user]
	if !ok {
		return game.Member{}, store.ErrNotFound
	}
	return m, nil
}

// SetEvents replaces a session's log, creating the session if needed.
func (s *MemStore) SetEvents(id string, evs []game.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[id] = evs
	if s.members[id] == nil {
		s.members[id] = map[game.UserID]game.Member{}
	}
}

func (s *MemStore) AddMember(id string, m game.Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[id][m.UserID] = m
}

// FailNextAppend makes the next Append return err.
func (s *MemStore) FailNextAppend(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendErr = err
}

// Len reports how many events a session has.
func (s *MemStore) Len(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events[id])
}

// Loads reports how many times a session was started from the store.
func (s *MemStore) Loads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}
