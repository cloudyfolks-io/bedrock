package methods

import (
	"fmt"
	"testing"
	"time"
)

func TestRateLimiterSlidingWindow(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !limiter.Allow("1.2.3.4", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("attempt %d must be allowed", i)
		}
	}
	if limiter.Allow("1.2.3.4", now.Add(3*time.Second)) {
		t.Fatal("the 4th attempt within the window must be refused")
	}
	if !limiter.Allow("5.6.7.8", now.Add(3*time.Second)) {
		t.Fatal("a different client IP must have its own window")
	}
	if !limiter.Allow("1.2.3.4", now.Add(61*time.Second)) {
		t.Fatal("an attempt after the window slides must be allowed again")
	}
}

func TestRateLimiterSweepsStaleKeysBeyondBound(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(1, time.Minute)
	for i := 0; i < 5000; i++ {
		limiter.Allow(fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256), now)
	}
	if len(limiter.hits) != 5000 {
		t.Fatalf("expected 5000 tracked keys before the sweep, got %d", len(limiter.hits))
	}
	later := now.Add(2 * time.Minute)
	if !limiter.Allow("1.2.3.4", later) {
		t.Fatal("a fresh key after the window must still be allowed")
	}
	if len(limiter.hits) != 1 {
		t.Fatalf("stale keys beyond the bound must be swept, got %d keys", len(limiter.hits))
	}
}

func TestRateLimiterSweepsAtMostOncePerWindow(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(1, time.Minute)
	limiter.Allow("first", start)
	limiter.Allow("middle", start.Add(30*time.Second))
	limiter.Allow("probe", start.Add(61*time.Second))
	if _, kept := limiter.hits["first"]; kept {
		t.Fatal("the sweep after one window must remove the stale key")
	}
	if _, kept := limiter.hits["middle"]; !kept {
		t.Fatal("a key inside the window must stay")
	}
	limiter.Allow("late", start.Add(100*time.Second))
	if _, kept := limiter.hits["middle"]; !kept {
		t.Fatal("a second sweep inside the same window must not happen")
	}
	limiter.Allow("later", start.Add(122*time.Second))
	if _, kept := limiter.hits["middle"]; kept {
		t.Fatal("the next window must sweep the stale key")
	}
}

func TestRateLimiterCapsTheNumberOfKeys(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(1, time.Minute)
	for i := 0; i < rateLimiterMaxKeys+1000; i++ {
		limiter.Allow(fmt.Sprintf("flood-%d", i), start)
	}
	if len(limiter.hits) > rateLimiterMaxKeys {
		t.Fatalf("keys = %d, want at most %d", len(limiter.hits), rateLimiterMaxKeys)
	}
	if limiter.Allow("one-more", start) {
		t.Fatal("a new key must be refused while the map is full")
	}
	if limiter.Allow("flood-0", start) {
		t.Fatal("a known key keeps its own limit while the map is full")
	}
	if !limiter.Allow("after-the-window", start.Add(2*time.Minute)) {
		t.Fatal("the map must accept new keys once the stale keys are swept")
	}
}
