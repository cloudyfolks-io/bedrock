package settings

import (
	"strings"
	"testing"
)

func providerPools(first, second, secondInterface string) string {
	return `[{"name":"a","interface":"bond0","providerName":"` + first + `","cidr":"10.0.0.0/24","gateway":"10.0.0.1"},` +
		`{"name":"b","interface":"` + secondInterface + `","providerName":"` + second + `","cidr":"10.0.1.0/24","gateway":"10.0.1.1"}]`
}

func TestDecodeProviderNetworksAcceptsOneNamePerInterface(t *testing.T) {
	if _, err := DecodeProviderNetworks(providerPools("public", "public", "bond0")); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeProviderNetworks(providerPools("public", "", "bond0")); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeProviderNetworksRejectsConflictingNames(t *testing.T) {
	cases := map[string]string{
		"two names on one interface":          providerPools("public", "edge", "bond0"),
		"one name on two interfaces":          providerPools("public", "public", "bond1"),
		"not a dns label":                     providerPools("Public_1", "Public_1", "bond0"),
		"name of a pool on another interface": providerPools("b", "", "bond1"),
	}
	for name, raw := range cases {
		if _, err := DecodeProviderNetworks(raw); err == nil || !strings.Contains(err.Error(), "providerName") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
