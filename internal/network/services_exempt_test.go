package network

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func servicesACLMatch(t *testing.T, values map[string]string) string {
	t.Helper()
	acls, _, _ := unstructured.NestedSlice(servicesSubnetOf(t, values).Object, "spec", "acls")
	if len(acls) != 1 {
		t.Fatalf("acls %v", acls)
	}
	match, _ := acls[0].(map[string]any)["match"].(string)
	return match
}

func TestServicesACLExemptsTheDatabaseVIPRangeAndTheIdentityRange(t *testing.T) {
	values := servicesValues(servicesL2Pools, map[string]string{"database.vip-range": "100.66.16.0/21"})
	want := "ip4.src == 100.66.0.0/15 && ip4.src != 100.66.0.0/24 && ip4.src != 100.66.16.0/21 && ip4.src != 100.67.0.0/16 && " +
		"ip4.dst == 100.66.0.0/15 && ip4.dst != 100.66.0.0/24 && ip4.dst != 100.66.16.0/21 && ip4.dst != 100.67.0.0/16"
	if got := servicesACLMatch(t, values); got != want {
		t.Fatalf("match %s", got)
	}
}

func TestServicesACLWithoutADatabaseRangeExemptsOnlyTheFirstNetworkAndTheIdentityRange(t *testing.T) {
	got := servicesACLMatch(t, servicesValues(servicesL2Pools, nil))
	if strings.Contains(got, "100.66.16.0") || !strings.Contains(got, "ip4.dst != 100.67.0.0/16") {
		t.Fatalf("match %s", got)
	}
}

func TestServicesACLIgnoresARangeOutsideThePool(t *testing.T) {
	values := servicesValues(servicesL2Pools, map[string]string{"database.vip-range": "10.20.0.0/24"})
	if got := servicesACLMatch(t, values); strings.Contains(got, "10.20.0.0") {
		t.Fatalf("match %s", got)
	}
}

func TestMissingServicesSettingsRefusesADatabaseRangeOutsideThePool(t *testing.T) {
	cfg := configOf(t, servicesValues(servicesL2Pools, map[string]string{"database.vip-range": "10.20.0.0/24"}))
	missing := MissingServicesSettings(cfg)
	if len(missing) != 1 || !strings.Contains(missing[0], "database.vip-range=10.20.0.0/24") {
		t.Fatalf("missing %v", missing)
	}
	inside := configOf(t, servicesValues(servicesL2Pools, map[string]string{"database.vip-range": "100.66.16.0/21"}))
	if missing := MissingServicesSettings(inside); len(missing) != 0 {
		t.Fatalf("missing %v", missing)
	}
}
