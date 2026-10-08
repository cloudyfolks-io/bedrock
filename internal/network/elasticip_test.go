package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestCheckAddress(t *testing.T) {
	provider := v1alpha1.ProviderNetworkSpec{Name: "public", CIDR: "192.0.2.0/24", Gateway: "192.0.2.1", ExcludeIPs: []string{"192.0.2.5..192.0.2.9"}}
	cases := map[string]string{
		"":             "",
		"192.0.2.50":   "",
		"198.51.100.5": ReasonAddressOutsideProviderNetwork,
		"192.0.2.1":    ReasonAddressReserved,
		"192.0.2.0":    ReasonAddressReserved,
		"192.0.2.255":  ReasonAddressReserved,
		"192.0.2.7":    ReasonAddressReserved,
		"nonsense":     ReasonAddressOutsideProviderNetwork,
	}
	for address, want := range cases {
		got := CheckAddress(provider, address)
		if (got == nil) != (want == "") || (got != nil && got.Reason != want) {
			t.Fatalf("address %q: got %+v want %q", address, got, want)
		}
	}
}

func TestRouterPortSubnet(t *testing.T) {
	if RouterPortSubnet(EIPModeL2, "public", "transit") != "provider-public" || RouterPortSubnet(EIPModeBGP, "pool", "transit") != "provider-transit" {
		t.Fatal("unexpected router port subnet")
	}
}

func connectedL2() ExternalPathInput {
	return ExternalPathInput{
		Mode: EIPModeL2, NetworkExternal: true, ProviderNetwork: "public",
		Vpc:        ObservedVpc{Standby: true, ExternalSubnets: []string{"provider-public"}},
		RouterPort: ObservedPort{Exists: true, Ready: true, Address: "192.0.2.2"},
	}
}

func TestExternalPathL2(t *testing.T) {
	notExternal := connectedL2()
	notExternal.NetworkExternal = false
	notStandby := connectedL2()
	notStandby.Vpc.Standby = false
	noSubnet := connectedL2()
	noSubnet.Vpc.ExternalSubnets = nil
	noPort := connectedL2()
	noPort.RouterPort = ObservedPort{}
	notReady := connectedL2()
	notReady.RouterPort.Ready = false
	noAddress := connectedL2()
	noAddress.RouterPort.Address = ""
	cases := map[string]struct {
		in   ExternalPathInput
		want string
	}{
		"not external": {notExternal, ReasonNetworkNotExternal}, "not standby": {notStandby, ReasonExternalNotConnected},
		"subnet missing": {noSubnet, ReasonExternalNotConnected}, "port missing": {noPort, ReasonExternalNotConnected},
		"port not ready": {notReady, ReasonExternalNotConnected}, "no address": {noAddress, ReasonExternalNotConnected},
		"connected": {connectedL2(), ""},
	}
	for name, tc := range cases {
		got := ExternalPath(tc.in)
		if (got == nil) != (tc.want == "") || (got != nil && got.Reason != tc.want) {
			t.Fatalf("%s: got %+v want %q", name, got, tc.want)
		}
	}
}

func TestExternalPathBGP(t *testing.T) {
	connected := ExternalPathInput{
		Mode: EIPModeBGP, NetworkExternal: true, ProviderNetwork: "pool", Transit: "transit",
		Vpc:        ObservedVpc{Standby: true, ExternalSubnets: []string{"provider-transit"}, DynamicRouting: true},
		RouterPort: ObservedPort{Exists: true, Ready: true, Address: "192.0.2.2"},
	}
	if got := ExternalPath(connected); got != nil {
		t.Fatalf("connected: %+v", got)
	}
	noRouting := connected
	noRouting.Vpc.DynamicRouting = false
	if got := ExternalPath(noRouting); got == nil || got.Reason != ReasonExternalNotConnected {
		t.Fatalf("no dynamic routing: %+v", got)
	}
	transit := connected
	transit.ProviderNetwork = "transit"
	if got := ExternalPath(transit); got == nil || got.Reason != ReasonUnsupported || got.Message != "the transit provider network cannot hold elastic IPs in bgp mode" {
		t.Fatalf("transit: %+v", got)
	}
}

func ipObject(name, namespace, pod, subnet string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"namespace": namespace, "podName": pod, "subnet": subnet}}}
	u.SetName(name)
	u.SetLabels(map[string]string{"fabric.cloudyfolks.io/subnet": subnet})
	return u
}

func TestFindIPMatchesVMNamespaceAndSubnet(t *testing.T) {
	ips := []*unstructured.Unstructured{ipObject("other", "ns", "vm2", "s1"), ipObject("wanted", "ns", "vm1", "s1")}
	if name, found := FindIP(ips, "ns", "vm1", "s1"); !found || name != "wanted" {
		t.Fatal(name, found)
	}
}

func TestFindIPIgnoresOtherSubnetsAndNamespaces(t *testing.T) {
	ips := []*unstructured.Unstructured{ipObject("a", "ns", "vm1", "s2"), ipObject("b", "elsewhere", "vm1", "s1")}
	if _, found := FindIP(ips, "ns", "vm1", "s1"); found {
		t.Fatal("must not match")
	}
	unlabeled := ipObject("c", "ns", "vm1", "s1")
	unlabeled.SetLabels(nil)
	if _, found := FindIP([]*unstructured.Unstructured{unlabeled}, "ns", "vm1", "s1"); found {
		t.Fatal("an IP without the subnet label must not match")
	}
}
