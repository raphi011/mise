package auth

import (
	"sync"
	"time"
)

// Limiter is an in-memory token bucket per key (client IP, username). Each
// key may spend burst attempts at once and regains one every interval.
//
// Memory is bounded: buckets that have refilled completely are idle and are
// dropped by a periodic sweep, and when maxKeys is reached arbitrary buckets
// are evicted. An evicted key starts again with a full bucket, which only
// matters while someone floods the limiter with more than maxKeys keys.
type Limiter struct {
	burst    float64
	interval time.Duration
	maxKeys  int
	now      func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewLimiter returns a Limiter. now may be nil to use time.Now.
func NewLimiter(burst int, interval time.Duration, maxKeys int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		burst:    float64(burst),
		interval: interval,
		maxKeys:  maxKeys,
		now:      now,
		buckets:  map[string]*bucket{},
	}
}

// Allow spends one attempt for key and reports whether it was available.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			l.evict()
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = l.refill(b, now)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) refill(b *bucket, now time.Time) float64 {
	return min(l.burst, b.tokens+float64(now.Sub(b.at))/float64(l.interval))
}

// sweep drops full buckets at most once per refill interval.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.interval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if l.refill(b, now) >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// evict makes room for one key when the map is full.
func (l *Limiter) evict() {
	for k := range l.buckets {
		delete(l.buckets, k)
		if len(l.buckets) < l.maxKeys {
			return
		}
	}
}

// Len reports the number of tracked keys, for tests.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
