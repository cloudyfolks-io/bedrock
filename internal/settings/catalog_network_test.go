package settings

import (
	"strings"
	"testing"
)

const publicNetwork = `[{"name":"public","interface":"bond0","vlan":100,"cidr":"91.246.31.0/24","gateway":"91.246.31.1","excludeIPs":["91.246.31.2..91.246.31.10"]}]`

func TestNetworkCatalogKeysAndDefaults(t *testing.T) {
	want := map[string]string{
		"network.eip-mode": "l2", "network.provider-networks": "", "network.bgp.local-asn": "", "network.bgp.router-id": "",
		"network.bgp.peers": "", "network.bgp.transit-network": "", "network.dns-servers": "",
	}
	for key, def := range want {
		got, ok := Lookup(key)
		if !ok || got.Default != def || got.Description == "" {
			t.Fatalf("setting %q: %+v", key, got)
		}
	}
}

func validates(t *testing.T, key, value string) error {
	t.Helper()
	def, ok := Lookup(key)
	if !ok {
		t.Fatalf("missing %s", key)
	}
	return def.Validate(value)
}

func TestEIPModeValidation(t *testing.T) {
	for value, valid := range map[string]bool{"": true, "l2": true, "bgp": true, "arp": false} {
		if (validates(t, "network.eip-mode", value) == nil) != valid {
			t.Fatalf("eip-mode %q valid=%v", value, valid)
		}
	}
}

func TestProviderNetworksValidation(t *testing.T) {
	long := strings.Repeat("a", 31)
	cases := map[string]struct {
		value string
		valid bool
	}{
		"empty":             {"", true},
		"valid":             {publicNetwork, true},
		"duplicate names":   {`[{"name":"a","interface":"b","cidr":"10.0.0.0/24","gateway":"10.0.0.1"},{"name":"a","interface":"c","cidr":"10.0.1.0/24","gateway":"10.0.1.1"}]`, false},
		"long name":         {`[{"name":"` + long + `","interface":"b","cidr":"10.0.0.0/24","gateway":"10.0.0.1"}]`, false},
		"long interface":    {`[{"name":"a","interface":"abcdefghijklmnop","cidr":"10.0.0.0/24","gateway":"10.0.0.1"}]`, false},
		"empty interface":   {`[{"name":"a","interface":"","cidr":"10.0.0.0/24","gateway":"10.0.0.1"}]`, false},
		"vlan too high":     {`[{"name":"a","interface":"b","vlan":4095,"cidr":"10.0.0.0/24","gateway":"10.0.0.1"}]`, false},
		"host bits":         {`[{"name":"a","interface":"b","cidr":"10.0.0.5/24","gateway":"10.0.0.1"}]`, false},
		"ipv6":              {`[{"name":"a","interface":"b","cidr":"fd00::/64","gateway":"fd00::1"}]`, false},
		"gateway outside":   {`[{"name":"a","interface":"b","cidr":"10.0.0.0/24","gateway":"10.0.1.1"}]`, false},
		"backwards range":   {`[{"name":"a","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1","excludeIPs":["91.246.31.10..91.246.31.2"]}]`, false},
		"exclude outside":   {`[{"name":"a","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1","excludeIPs":["91.246.32.5"]}]`, false},
		"range end outside": {`[{"name":"a","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1","excludeIPs":["91.246.31.5..91.246.32.5"]}]`, false},
		"single address":    {`[{"name":"a","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1","excludeIPs":["91.246.31.5"]}]`, true},
		"not json":          {`public`, false},
		"unknown field":     {`[{"name":"a","interface":"b","cidr":"10.0.0.0/24","gateway":"10.0.0.1","color":"red"}]`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validates(t, "network.provider-networks", tc.value); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestBGPPeersValidation(t *testing.T) {
	for value, valid := range map[string]bool{"": true, "10.0.0.1@65000,10.0.0.2@65000": true, "10.0.0.1": false, "x@1": false, "10.0.0.1@0": false, "fd00::1@65000": false} {
		if (validates(t, "network.bgp.peers", value) == nil) != valid {
			t.Fatalf("peers %q valid=%v", value, valid)
		}
	}
}

func TestIPv4ListValidation(t *testing.T) {
	for value, valid := range map[string]bool{"": true, "1.1.1.1": true, "1.1.1.1,8.8.8.8": true, "1.1.1.1,": false, "dns.example": false, "fd00::1": false} {
		if (validates(t, "network.dns-servers", value) == nil) != valid {
			t.Fatalf("dns-servers %q valid=%v", value, valid)
		}
	}
	if validates(t, "network.bgp.router-id", "10.0.0.9") != nil || validates(t, "network.bgp.router-id", "x") == nil {
		t.Fatal("router id must be an IPv4 address")
	}
}

func TestBGPASNValidation(t *testing.T) {
	for value, valid := range map[string]bool{"": true, "1": true, "4294967295": true, "0": false, "4294967296": false, "x": false} {
		if (validates(t, "network.bgp.local-asn", value) == nil) != valid {
			t.Fatalf("asn %q valid=%v", value, valid)
		}
	}
}

func TestTransitNetworkValidation(t *testing.T) {
	if validates(t, "network.bgp.transit-network", "transit") != nil || validates(t, "network.bgp.transit-network", "Not_A_Label") == nil {
		t.Fatal("transit must be a DNS label")
	}
}

func TestParseAddressRange(t *testing.T) {
	first, last, err := ParseAddressRange("10.0.0.2..10.0.0.9")
	if err != nil || first.String() != "10.0.0.2" || last.String() != "10.0.0.9" {
		t.Fatal(first, last, err)
	}
	first, last, err = ParseAddressRange("10.0.0.2")
	if err != nil || first != last {
		t.Fatal(first, last, err)
	}
}
