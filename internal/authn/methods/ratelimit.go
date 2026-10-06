package methods

import (
	"sync"
	"time"
)

const rateLimiterMaxKeys = 65536

type RateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	hits      map[string][]time.Time
	nextSweep time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}

func (l *RateLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	if !now.Before(l.nextSweep) {
		l.sweep(cutoff)
		l.nextSweep = now.Add(l.window)
	}
	known := len(l.hits[key]) != 0
	if !known && len(l.hits) >= rateLimiterMaxKeys {
		return false
	}
	kept := pruneBefore(l.hits[key], cutoff)
	if len(kept) >= l.limit {
		l.hits[key] = kept
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
