package methods

import (
	"sync"
	"time"
)

const rateLimiterSweepThreshold = 4096

type RateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}

func (l *RateLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := pruneBefore(l.hits[key], cutoff)
	if len(kept) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = kept
	}
	if len(l.hits) > rateLimiterSweepThreshold {
		l.sweep(cutoff)
	}
	kept = l.hits[key]
	if len(kept) >= l.limit {
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

func (l *RateLimiter) sweep(cutoff time.Time) {
	for key, hits := range l.hits {
		kept := pruneBefore(hits, cutoff)
		if len(kept) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = kept
		}
	}
}

func pruneBefore(hits []time.Time, cutoff time.Time) []time.Time {
	kept := hits[:0]
	for _, seen := range hits {
		if seen.After(cutoff) {
			kept = append(kept, seen)
		}
	}
	return kept
}
