package methods

import (
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
