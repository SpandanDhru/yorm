package ws

import "time"

// bucket is a token bucket: it holds up to burst tokens and refills at rate
// per second; each command takes one. Only the connection's reader uses it,
// so it needs no lock.
type bucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newBucket(rate float64, burst int, now func() time.Time) *bucket {
	return &bucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now(), now: now}
}

// take reports whether a command may go ahead, using up a token if so.
func (b *bucket) take() bool {
	now := b.now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
