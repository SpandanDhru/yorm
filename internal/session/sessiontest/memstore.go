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
	appendErr error
	loads     int
}

// NewMemStore returns a store holding the given empty sessions.
func NewMemStore(sessions ...string) *MemStore {
	s := &MemStore{events: map[string][]game.Event{}, members: map[string]map[game.UserID]game.Member{}}
	for _, id := range sessions {
		s.events[id] = nil
		s.members[id] = map[game.UserID]game.Member{}
	}
	return s
}

func (s *MemStore) Load(_ context.Context, id string) ([]game.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, ok := s.events[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	s.loads++
	return append([]game.Event(nil), evs...), nil
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

// Loads reports how many times any session was loaded.
func (s *MemStore) Loads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}
