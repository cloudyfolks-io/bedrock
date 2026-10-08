package network

import (
	"slices"
	"testing"
)

func TestReservedRangesListsPlatformRanges(t *testing.T) {
	cfg := configOf(t, map[string]string{"network.provider-networks": publicNetworks})
	got := ReservedRanges(cfg, "100.64.0.0/16", "10.0.10.10", []string{"10.0.10.11", "10.0.10.12"})
	names := make([]string, len(got))
	for i, r := range got {
		names[i] = r.Name + " " + r.CIDR
	}
	want := []string{
		"provider network public 91.246.31.0/24", "join subnet 100.64.0.0/16", "management 10.0.10.10/32",
		"management 10.0.10.11/32", "management 10.0.10.12/32",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("ranges %v", names)
	}
}

func TestReservedRangesSkipsEmptyAndInvalidAddresses(t *testing.T) {
	got := ReservedRanges(configOf(t, map[string]string{}), "", "", []string{"", "nonsense"})
	if len(got) != 0 {
		t.Fatalf("ranges %v", got)
	}
}

func TestReservedOverlap(t *testing.T) {
	reserved := []ReservedRange{{Name: "provider network public", CIDR: "91.246.31.0/24"}, {Name: "management", CIDR: "10.0.10.10/32"}}
	cases := map[string]string{
		"91.246.31.0/28": ReasonCIDRReserved, "91.246.0.0/16": ReasonCIDRReserved, "10.0.10.0/24": ReasonCIDRReserved,
		"10.100.1.0/24": "", "91.246.32.0/24": "", "nonsense": "",
	}
	for cidr, want := range cases {
		got := ReservedOverlap(cidr, reserved)
		if (got == nil) != (want == "") || (got != nil && got.Reason != want) {
			t.Fatalf("%s: %+v", cidr, got)
		}
	}
}
