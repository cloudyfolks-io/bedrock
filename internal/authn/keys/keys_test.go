package keys

import (
	"crypto/elliptic"
	"crypto/rand"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func schedule(t *testing.T, starts ...time.Time) []Key {
	t.Helper()
	generated := make([]Key, 0, len(starts))
	for _, start := range starts {
		key, err := Generate(rand.Reader, start)
		if err != nil {
			t.Fatal(err)
		}
		generated = append(generated, key)
	}
	return generated
}

func ids(keys []Key) []string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.ID)
	}
	return names
}

func TestGenerate(t *testing.T) {
	key, err := Generate(rand.Reader, t0.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if key.ID != "k-20260928t120000z" || !key.NotBefore.Equal(t0) || !key.RetireAfter.Equal(t0.Add(Lifetime)) || key.Private.Curve != elliptic.P256() {
		t.Fatalf("key %s %v %v", key.ID, key.NotBefore, key.RetireAfter)
	}
}

func TestSigningPicksNewestActive(t *testing.T) {
	keys := schedule(t, t0, t0.Add(Lifetime), t0.Add(10*day))
	cases := []struct {
		now  time.Time
		want string
		ok   bool
	}{
		{t0.Add(-time.Second), "", false},
		{t0, keys[0].ID, true},
		{t0.Add(9 * day), keys[0].ID, true},
		{t0.Add(11 * day), keys[2].ID, true},
		{t0.Add(Lifetime), keys[1].ID, true},
		{t0.Add(2 * Lifetime), "", false},
	}
	for _, tc := range cases {
		key, ok := Signing(keys, tc.now)
		if ok != tc.ok || key.ID != tc.want {
			t.Errorf("at %v: signing %q %v, want %q %v", tc.now, key.ID, ok, tc.want, tc.ok)
		}
	}
	if _, ok := Signing(nil, t0); ok {
		t.Fatal("no keys cannot sign")
	}
}

func TestPublishedIncludesFutureAndRetired(t *testing.T) {
	keys := schedule(t, t0.Add(Lifetime), t0)
	cases := []struct {
		now  time.Time
		want []string
	}{
		{t0.Add(29 * day), []string{keys[1].ID, keys[0].ID}},
		{t0.Add(45 * day), []string{keys[1].ID, keys[0].ID}},
		{t0.Add(60 * day), []string{keys[0].ID}},
		{t0.Add(90 * day), []string{}},
	}
	for _, tc := range cases {
		if got := ids(Published(keys, tc.now)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("at %v: published %v, want %v", tc.now, got, tc.want)
		}
	}
	if keys[0].ID != "k-20261028t120000z" {
		t.Fatalf("Published must not reorder its input, got %v", ids(keys))
	}
}

func TestNextNotBefore(t *testing.T) {
	if next, ok := NextNotBefore(nil, t0); !ok || !next.Equal(t0) {
		t.Fatalf("no keys: %v %v, want now", next, ok)
	}
	one := schedule(t, t0)
	cases := []struct {
		now  time.Time
		want time.Time
		ok   bool
	}{
		{t0.Add(day), time.Time{}, false},
		{t0.Add(29*day - time.Second), time.Time{}, false},
		{t0.Add(29 * day), t0.Add(Lifetime), true},
		{t0.Add(40 * day), t0.Add(40 * day), true},
	}
	for _, tc := range cases {
		next, ok := NextNotBefore(one, tc.now)
		if ok != tc.ok || !next.Equal(tc.want) {
			t.Errorf("at %v: next %v %v, want %v %v", tc.now, next, ok, tc.want, tc.ok)
		}
	}
	two := schedule(t, t0, t0.Add(Lifetime))
	if _, ok := NextNotBefore(two, t0.Add(29*day)); ok {
		t.Fatal("a published successor needs no new key")
	}
}

func TestExpired(t *testing.T) {
	keys := schedule(t, t0, t0.Add(Lifetime))
	if got := Expired(keys, t0.Add(60*day-time.Second)); len(got) != 0 {
		t.Fatalf("nothing expires before retirement plus 30 days, got %v", ids(got))
	}
	if got := ids(Expired(keys, t0.Add(60*day))); !reflect.DeepEqual(got, []string{keys[0].ID}) {
		t.Fatalf("expired %v", got)
	}
}
