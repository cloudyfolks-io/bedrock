package schedule

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestValidate(t *testing.T) {
	for _, expr := range []string{"0 2 * * *", "*/15 * * * *", "0 0 29 2 *", "30 3 * * 1-5"} {
		if err := Validate(expr); err != nil {
			t.Fatalf("%q: %v", expr, err)
		}
	}
	for _, expr := range []string{"", "@daily", "@every 1h", "* * * * * *", "60 * * * *", "0 2 * *", "a b c d e"} {
		if err := Validate(expr); err == nil {
			t.Fatalf("%q must be invalid", expr)
		}
	}
}

func TestNext(t *testing.T) {
	cases := []struct{ after, want string }{
		{"2026-10-08T01:00:00Z", "2026-10-08T02:00:00Z"},
		{"2026-10-08T02:00:00Z", "2026-10-09T02:00:00Z"},
		{"2026-10-08T05:00:00+03:30", "2026-10-08T02:00:00Z"},
	}
	for _, tc := range cases {
		got, err := Next("0 2 * * *", at(tc.after))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(at(tc.want)) || got.Location() != time.UTC {
			t.Fatalf("after %s: got %s want %s", tc.after, got, tc.want)
		}
	}
}

func TestMinInterval(t *testing.T) {
	from := at("2026-10-08T00:00:00Z")
	cases := []struct {
		expr string
		want time.Duration
	}{
		{"* * * * *", time.Minute},
		{"*/5 * * * *", 5 * time.Minute},
		{"0 * * * *", time.Hour},
		{"0 0,1 * * *", time.Hour},
		{"0 2 * * *", 24 * time.Hour},
	}
	for _, tc := range cases {
		got, err := MinInterval(tc.expr, from)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.expr, got, tc.want)
		}
	}
	sparse, err := MinInterval("0 0 29 2 *", from)
	if err != nil || sparse <= 24*time.Hour {
		t.Fatalf("sparse: got %s, %v", sparse, err)
	}
}

func TestDue(t *testing.T) {
	cases := []struct {
		name      string
		last, now string
		want      bool
	}{
		{"zero last", "", "2026-10-08T02:30:00Z", true},
		{"same slot", "2026-10-08T02:00:00Z", "2026-10-08T02:30:00Z", false},
		{"next slot equals now", "2026-10-07T02:00:00Z", "2026-10-08T02:00:00Z", true},
		{"clock moved back", "2026-10-09T02:00:00Z", "2026-10-08T02:00:00Z", false},
	}
	for _, tc := range cases {
		var last time.Time
		if tc.last != "" {
			last = at(tc.last)
		}
		got, err := Due("0 2 * * *", last, at(tc.now))
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %v, %v", tc.name, got, err)
		}
	}
	if _, err := Due("@daily", time.Time{}, time.Now()); err == nil {
		t.Fatal("invalid expression must fail")
	}
}
