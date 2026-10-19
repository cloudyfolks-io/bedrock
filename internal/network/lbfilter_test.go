package network

import (
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

func entryMatches(t *testing.T, entry string, route netip.Prefix) bool {
	t.Helper()
	fields := strings.Fields(entry)
	base := netip.MustParsePrefix(fields[0])
	low, high := base.Bits(), 32
	if len(fields) == 1 {
		high = low
	}
	for i := 1; i < len(fields); i += 2 {
		bound, err := strconv.Atoi(fields[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if fields[i] == "ge" {
			low = bound
		} else {
			high = bound
		}
	}
	return base.Contains(route.Addr()) && route.Bits() >= base.Bits() && route.Bits() >= low && route.Bits() <= high
}

func filterMatches(t *testing.T, filter []string, route netip.Prefix) bool {
	t.Helper()
	for _, entry := range filter {
		if entryMatches(t, entry, route) {
			return true
		}
	}
	return false
}

var sampleRoutes = []string{
	"100.69.0.0/24", "100.69.0.5/32", "100.69.0.128/25", "100.69.0.255/32", "100.69.1.0/24", "100.69.1.7/32", "100.69.0.0/23",
	"100.68.255.255/32", "100.70.0.0/32", "100.64.0.0/10", "100.0.0.0/8", "10.0.0.0/8", "10.1.2.0/24", "10.1.2.3/32",
	"91.246.31.0/24", "91.246.31.20/32", "172.16.0.0/12", "192.168.0.0/16", "0.0.0.0/0", "255.255.255.255/32", "128.0.0.0/1",
}

func requirePlaneDenied(t *testing.T, original, got []string, plane netip.Prefix) {
	t.Helper()
	for _, text := range sampleRoutes {
		route := netip.MustParsePrefix(text)
		inside := route.Bits() >= plane.Bits() && plane.Contains(route.Addr())
		covers := route.Bits() < plane.Bits() && route.Contains(plane.Addr())
		allowed := filterMatches(t, got, route)
		switch {
		case inside && allowed:
			t.Fatalf("%s is inside %s and must not match %v", text, plane, got)
		case !inside && !covers && allowed != filterMatches(t, original, route):
			t.Fatalf("%s: filter %v changed the verdict of %v", text, got, original)
		}
	}
}

func TestFilterWithoutDeniesThePlaneInACustomFilter(t *testing.T) {
	plane := netip.MustParsePrefix("100.69.0.0/24")
	cases := map[string][]string{
		"disjoint entries":        {"91.246.31.0/24 ge 32 le 32", "10.0.0.0/8 ge 24 le 32"},
		"an entry that covers":    {"100.0.0.0/8 ge 8 le 32"},
		"an entry inside":         {"100.69.0.0/24 ge 32 le 32"},
		"an entry inside the net": {"100.69.0.128/25 ge 25 le 32", "91.246.31.0/24 ge 32 le 32"},
		"the default route":       {"0.0.0.0/0 ge 0 le 32"},
		"a bounded wide entry":    {"100.64.0.0/10 ge 24 le 28"},
	}
	for name, filter := range cases {
		t.Run(name, func(t *testing.T) {
			requirePlaneDenied(t, filter, FilterWithout(filter, plane), plane)
		})
	}
}

func TestFilterWithoutKeepsAnEmptyFilterEmpty(t *testing.T) {
	if got := FilterWithout(nil, netip.MustParsePrefix("100.69.0.0/24")); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestFilterWithoutDeniesThePlaneInAFilterOfAllRoutes(t *testing.T) {
	plane := netip.MustParsePrefix("100.69.0.0/24")
	all := []string{"0.0.0.0/0 ge 0 le 32"}
	got := FilterWithout(all, plane)
	requirePlaneDenied(t, all, got, plane)
	for _, route := range []string{"10.0.0.0/8", "91.246.31.20/32", "100.68.0.0/16", "100.70.0.0/24", "192.168.1.0/24"} {
		if !filterMatches(t, got, netip.MustParsePrefix(route)) {
			t.Fatalf("%s must still match %v", route, got)
		}
	}
}

func TestFilterWithoutKeepsAnUntouchedEntryAsWritten(t *testing.T) {
	plane := netip.MustParsePrefix("100.69.0.0/24")
	got := FilterWithout([]string{"91.246.31.0/24 ge 32 le 32"}, plane)
	if len(got) != 1 || got[0] != "91.246.31.0/24 ge 32 le 32" {
		t.Fatalf("got %v", got)
	}
}

func TestFilterWithoutNeverReturnsAnEmptyFilter(t *testing.T) {
	plane := netip.MustParsePrefix("100.69.0.0/24")
	got := FilterWithout([]string{"100.69.0.0/25 ge 32 le 32"}, plane)
	if len(got) != 1 {
		t.Fatalf("an empty filter advertises every route, got %v", got)
	}
	for _, route := range sampleRoutes {
		if filterMatches(t, got, netip.MustParsePrefix(route)) {
			t.Fatalf("%s must not match %v", route, got)
		}
	}
}

func TestFilterWithoutNeverWritesGeAtTheMaskLength(t *testing.T) {
	plane := netip.MustParsePrefix("100.69.0.0/24")
	for _, entry := range FilterWithout([]string{"0.0.0.0/0 ge 0 le 32"}, plane) {
		fields := strings.Fields(entry)
		base := netip.MustParsePrefix(fields[0])
		if len(fields)%2 == 0 || base.Masked() != base {
			t.Fatalf("entry %q", entry)
		}
		for i := 1; i < len(fields); i += 2 {
			bound, _ := strconv.Atoi(fields[i+1])
			if fields[i] == "ge" && bound <= base.Bits() {
				t.Fatalf("entry %q: ge must be above the mask length", entry)
			}
		}
	}
}
