package bot

import (
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Stats is shared by every bot in a run.
type Stats struct {
	Commands    atomic.Int64 // sent, including resends' originals only once
	Acks        atomic.Int64
	Rejects     atomic.Int64
	Events      atomic.Int64 // event messages received, across all bots
	Gaps        atomic.Int64 // an event arrived out of order, or a catch-up came up short
	Disconnects atomic.Int64
	Reconnects  atomic.Int64
	Errors      atomic.Int64 // unreadable messages or error replies

	sent  sync.Map // command ID -> time sent, to measure latency
	acked sync.Map // command ID -> seq it was accepted at

	mu        sync.Mutex
	latencies []float64 // milliseconds
	codes     map[string]int64
}

func NewStats() *Stats {
	return &Stats{codes: map[string]int64{}}
}

func (s *Stats) latency(d time.Duration) {
	s.mu.Lock()
	s.latencies = append(s.latencies, float64(d.Microseconds())/1000)
	s.mu.Unlock()
}

// Acked returns every accepted command's ID and the seq its answer gave.
func (s *Stats) Acked() map[string]int64 {
	out := map[string]int64{}
	s.acked.Range(func(k, v any) bool {
		out[k.(string)] = v.(int64)
		return true
	})
	return out
}

// ResetLatency forgets latencies measured so far, e.g. during setup.
func (s *Stats) ResetLatency() {
	s.mu.Lock()
	s.latencies = nil
	s.mu.Unlock()
}

func (s *Stats) rejected(code string) {
	s.mu.Lock()
	s.codes[code]++
	s.mu.Unlock()
}

// Report is a summary of a run.
type Report struct {
	Commands    int64            `json:"commands"`
	Acks        int64            `json:"acks"`
	Rejects     int64            `json:"rejects"`
	RejectCodes map[string]int64 `json:"reject_codes"`
	Events      int64            `json:"events_received"`
	Deliveries  int              `json:"deliveries_timed"` // events matched to a command sent here
	P50         float64          `json:"p50_ms"`
	P95         float64          `json:"p95_ms"`
	P99         float64          `json:"p99_ms"`
	Max         float64          `json:"max_ms"`
	Gaps        int64            `json:"gaps"`
	Disconnects int64            `json:"disconnects"`
	Reconnects  int64            `json:"reconnects"`
	Errors      int64            `json:"errors"`
}

func (s *Stats) Report() Report {
	s.mu.Lock()
	lat := slices.Clone(s.latencies)
	codes := maps.Clone(s.codes)
	s.mu.Unlock()
	slices.Sort(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(p*float64(len(lat))))]
	}
	r := Report{
		Commands: s.Commands.Load(), Acks: s.Acks.Load(), Rejects: s.Rejects.Load(), RejectCodes: codes,
		Events: s.Events.Load(), Deliveries: len(lat),
		P50: pct(0.50), P95: pct(0.95), P99: pct(0.99), Max: pct(1),
		Gaps: s.Gaps.Load(), Disconnects: s.Disconnects.Load(), Reconnects: s.Reconnects.Load(), Errors: s.Errors.Load(),
	}
	return r
}
