package network

import (
	"slices"
	"strings"
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
		"provider network public 91.246.31.0/24", "reserved 100.64.0.0/10 100.64.0.0/10", "load balancer plane 100.69.0.0/24", "join subnet 100.64.0.0/16", "management 10.0.10.10/32",
		"management 10.0.10.11/32", "management 10.0.10.12/32",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("ranges %v", names)
	}
}

func TestReservedRangesSkipsEmptyAndInvalidAddresses(t *testing.T) {
	got := ReservedRanges(configOf(t, map[string]string{}), "", "", []string{"", "nonsense"})
	if len(got) != 2 || got[0].CIDR != "100.64.0.0/10" || got[1].CIDR != "100.69.0.0/24" {
		t.Fatalf("only the reserved cidrs setting and the load balancer plane are left: %v", got)
	}
}

func TestSettingReservedHoldsOnlyTheReservedCIDRs(t *testing.T) {
	cfg := configOf(t, map[string]string{"network.provider-networks": publicNetworks, "network.reserved-cidrs": "100.64.0.0/10,10.16.0.0/16"})
	got := SettingReserved(cfg)
	names := make([]string, len(got))
	for i, r := range got {
		names[i] = r.Name + " " + r.CIDR
	}
	want := []string{"reserved 100.64.0.0/10 100.64.0.0/10", "reserved 10.16.0.0/16 10.16.0.0/16"}
	if !slices.Equal(names, want) {
		t.Fatalf("ranges %v", names)
	}
}

func TestReservedOverlap(t *testing.T) {
	reserved := []ReservedRange{{Name: "provider network public", CIDR: "91.246.31.0/24"}, {Name: "management", CIDR: "10.0.10.10/32"}, {Name: "reserved 100.64.0.0/10", CIDR: "100.64.0.0/10"}}
	cases := map[string]string{
		"91.246.31.0/28": ReasonCIDRReserved, "91.246.0.0/16": ReasonCIDRReserved, "10.0.10.0/24": ReasonCIDRReserved,
		"100.64.1.0/24": ReasonCIDRReserved, "100.0.0.0/8": ReasonCIDRReserved,
		"10.100.1.0/24": "", "91.246.32.0/24": "", "100.128.0.0/24": "", "nonsense": "",
	}
	for cidr, want := range cases {
		got := ReservedOverlap(cidr, reserved)
		if (got == nil) != (want == "") || (got != nil && got.Reason != want) {
			t.Fatalf("%s: %+v", cidr, got)
		}
	}
}

func TestReservedOverlapNamesTheRange(t *testing.T) {
	got := ReservedOverlap("100.64.1.0/24", []ReservedRange{{Name: "reserved 100.64.0.0/10", CIDR: "100.64.0.0/10"}})
	if got == nil || !strings.Contains(got.Message, "reserved 100.64.0.0/10") || !strings.Contains(got.Message, "Subnet") {
		t.Fatalf("verdict %+v", got)
	}
}

func TestNetworkReservedOverlap(t *testing.T) {
	reserved := []ReservedRange{{Name: "reserved 100.64.0.0/10", CIDR: "100.64.0.0/10"}, {Name: "provider network public", CIDR: "91.246.31.0/24"}}
	cases := map[string]struct {
		ranges []string
		bad    string
	}{
		"private ranges":      {[]string{"10.100.0.0/16", "172.16.0.0/16", "192.168.0.0/24"}, ""},
		"no ranges":           {nil, ""},
		"inside reserved":     {[]string{"10.100.0.0/16", "100.64.8.0/24"}, "100.64.8.0/24"},
		"around reserved":     {[]string{"100.0.0.0/8"}, "100.0.0.0/8"},
		"another range":       {[]string{"91.246.31.0/25"}, "91.246.31.0/25"},
		"unparsable is a gap": {[]string{"nonsense"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NetworkReservedOverlap(tc.ranges, reserved)
			if (got == nil) != (tc.bad == "") {
				t.Fatalf("verdict %+v", got)
			}
			if got != nil && (got.Reason != ReasonCIDRReserved || !strings.Contains(got.Message, tc.bad) || !strings.Contains(got.Message, "Network")) {
				t.Fatalf("verdict %+v", got)
			}
		})
	}
}

func TestAllowedRangeViolation(t *testing.T) {
	allowed := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	cases := map[string]bool{
		"10.100.1.0/24": false, "172.31.255.0/24": false, "192.168.0.0/16": false, "10.0.0.0/8": false,
		"100.64.1.0/24": true, "172.32.0.0/24": true, "8.8.8.0/24": true, "192.169.0.0/24": true, "10.0.0.0/7": true,
	}
	for cidr, bad := range cases {
		got := AllowedRangeViolation(cidr, allowed)
		if (got != nil) != bad || (got != nil && (got.Reason != ReasonCIDROutsideAllowedRanges || !strings.Contains(got.Message, "network.subnet-allowed-ranges"))) {
			t.Fatalf("%s: %+v", cidr, got)
		}
	}
	if got := AllowedRangeViolation("nonsense", allowed); got != nil {
		t.Fatalf("an unparsable cidr is judged elsewhere: %+v", got)
	}
}

func TestPlaneReservedFollowsTheLoadBalancerSetting(t *testing.T) {
	cfg := configOf(t, map[string]string{"network.load-balancer.cidr": "100.70.0.0/16", "network.load-balancer.gateway": "100.70.0.1"})
	got := PlaneReserved(cfg)
	if len(got) != 1 || got[0].Name != "load balancer plane" || got[0].CIDR != "100.70.0.0/16" {
		t.Fatalf("plane %v", got)
	}
	if got := PlaneReserved(Config{}); len(got) != 0 {
		t.Fatalf("an unset plane reserves nothing: %v", got)
	}
}
