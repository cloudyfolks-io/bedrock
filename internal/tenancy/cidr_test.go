package tenancy

import (
	"errors"
	"net/netip"
	"testing"
)

func prefixes(t *testing.T, texts ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(texts))
	for _, text := range texts {
		out = append(out, netip.MustParsePrefix(text))
	}
	return out
}

func TestNextFreeSubnet(t *testing.T) {
	vpc := netip.MustParsePrefix("10.100.0.0/16")
	cases := []struct {
		name string
		used []string
		want string
	}{
		{"first free", nil, "10.100.0.0/24"},
		{"skips used", []string{"10.100.0.0/24", "10.100.1.0/24"}, "10.100.2.0/24"},
		{"fills gaps", []string{"10.100.0.0/24", "10.100.2.0/24"}, "10.100.1.0/24"},
		{"skips a wider block", []string{"10.100.0.0/23"}, "10.100.2.0/24"},
		{"skips a narrower block", []string{"10.100.0.64/26"}, "10.100.1.0/24"},
		{"skips two partial blocks", []string{"10.100.1.0/25", "10.100.0.0/24"}, "10.100.2.0/24"},
		{"ignores outside the vpc", []string{"192.168.0.0/24", "10.101.0.0/24"}, "10.100.0.0/24"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextFreeSubnet(vpc, prefixes(t, c.used...))
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestNextFreeSubnetIsOrderIndependent(t *testing.T) {
	vpc := netip.MustParsePrefix("10.100.0.0/16")
	orders := [][]string{
		{"10.100.0.0/24", "10.100.1.0/25", "10.100.3.0/24"},
		{"10.100.3.0/24", "10.100.0.0/24", "10.100.1.0/25"},
		{"10.100.1.0/25", "10.100.3.0/24", "10.100.0.0/24"},
	}
	for _, used := range orders {
		got, err := NextFreeSubnet(vpc, prefixes(t, used...))
		if err != nil || got.String() != "10.100.2.0/24" {
			t.Fatalf("got %v, %v", got, err)
		}
	}
}

func TestNextFreeSubnetExhausted(t *testing.T) {
	full := make([]netip.Prefix, 0, 256)
	for third := 0; third < 256; third++ {
		full = append(full, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 100, byte(third), 0}), 24))
	}
	cases := []struct {
		name string
		vpc  string
		used []netip.Prefix
	}{
		{"one block", "10.100.0.0/24", prefixes(t, "10.100.0.0/24")},
		{"all blocks", "10.100.0.0/16", full},
		{"containing block", "10.100.0.0/16", prefixes(t, "10.100.0.0/15")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NextFreeSubnet(netip.MustParsePrefix(c.vpc), c.used)
			if !errors.Is(err, ErrNoFreeSubnet) {
				t.Fatalf("err = %v, want ErrNoFreeSubnet", err)
			}
		})
	}
}

func TestNextFreeSubnetRefusesABadVPC(t *testing.T) {
	for _, vpc := range []string{"10.100.0.0/25", "fd00::/48"} {
		_, err := NextFreeSubnet(netip.MustParsePrefix(vpc), nil)
		if err == nil || errors.Is(err, ErrNoFreeSubnet) {
			t.Fatalf("vpc %s: err = %v", vpc, err)
		}
	}
}

func TestNextFreeSubnetMasksTheVPC(t *testing.T) {
	got, err := NextFreeSubnet(netip.MustParsePrefix("10.100.5.7/16"), nil)
	if err != nil || got.String() != "10.100.0.0/24" {
		t.Fatalf("got %v, %v", got, err)
	}
}
