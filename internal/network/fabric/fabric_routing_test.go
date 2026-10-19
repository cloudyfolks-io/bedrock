package fabric

import "testing"

func TestVpcBuildsPeeringsAndStaticRoutes(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{
		EnableExternal:       true,
		ExtraExternalSubnets: []string{"provider-public"},
		DefaultRoute:         &StaticRoute{CIDR: "0.0.0.0/0", NextHop: "192.0.2.1"},
		StaticRoutes:         []StaticRoute{{CIDR: "10.2.0.0/16", NextHop: "169.254.0.2"}},
		Peerings:             []Peering{{RemoteVpc: "other", LocalConnectIP: "169.254.0.1/30"}},
	})
	equal(t, spec(t, u), map[string]any{
		"enableExternal":       true,
		"extraExternalSubnets": []any{"provider-public"},
		"staticRoutes": []any{
			map[string]any{"policy": "policyDst", "cidr": "0.0.0.0/0", "nextHopIP": "192.0.2.1"},
			map[string]any{"policy": "policyDst", "cidr": "10.2.0.0/16", "nextHopIP": "169.254.0.2"},
		},
		"vpcPeerings": []any{map[string]any{"remoteVpc": "other", "localConnectIP": "169.254.0.1/30"}},
	})
}

func TestVpcWritesPeeringsWithoutTheExternalConnection(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{Peerings: []Peering{{RemoteVpc: "other", LocalConnectIP: "169.254.0.1/30"}}})
	equal(t, spec(t, u), map[string]any{"vpcPeerings": []any{map[string]any{"remoteVpc": "other", "localConnectIP": "169.254.0.1/30"}}})
}

func TestVpcPeeringsStatusIsRead(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{})
	u.Object["status"] = map[string]any{"vpcPeerings": []any{"other"}}
	if got := VpcStatusPeerings(u); len(got) != 1 || got[0] != "other" {
		t.Fatalf("peerings %v", got)
	}
}
