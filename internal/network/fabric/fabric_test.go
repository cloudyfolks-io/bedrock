package fabric

import (
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func spec(t *testing.T, u *unstructured.Unstructured) map[string]any {
	t.Helper()
	got, _, err := unstructured.NestedMap(u.Object, "spec")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func equal(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestVpcBuildsPrivateSpec(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{})
	equal(t, spec(t, u), map[string]any{})
	if u.GetKind() != "Vpc" || u.GetAPIVersion() != "fabric.cloudyfolks.io/v1" || u.GetName() != "acme" {
		t.Fatalf("unexpected identity %v", u.Object)
	}
}

func TestVpcBuildsL2Spec(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{EnableExternal: true, ExtraExternalSubnets: []string{"provider-public"}, DefaultRoute: &StaticRoute{CIDR: "0.0.0.0/0", NextHop: "192.0.2.1"}})
	equal(t, spec(t, u), map[string]any{
		"enableExternal":       true,
		"extraExternalSubnets": []any{"provider-public"},
		"staticRoutes":         []any{map[string]any{"policy": "policyDst", "cidr": "0.0.0.0/0", "nextHopIP": "192.0.2.1"}},
	})
}

func TestVpcBuildsBGPSpec(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{EnableExternal: true, EnableBfd: true, ExtraExternalSubnets: []string{"provider-transit"}, Dynamic: &DynamicRouting{VrfID: 1000, ExternalSubnet: "provider-transit", Redistribute: []string{"nat", "lb"}, LocalOnly: true}})
	got := spec(t, u)
	if got["enableBfd"] != true {
		t.Fatalf("unexpected spec %#v", got)
	}
	equal(t, got["dynamicRouting"].(map[string]any), map[string]any{"enabled": true, "redistribute": []any{"nat", "lb"}, "vrfId": int64(1000), "externalSubnet": "provider-transit", "localOnly": true})
}

func TestVpcOmitsBfdAndLocalOnlyByDefault(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{EnableExternal: true, Dynamic: &DynamicRouting{VrfID: 1000, ExternalSubnet: "provider-transit", Redistribute: []string{"nat"}}})
	got := spec(t, u)
	if _, found := got["enableBfd"]; found {
		t.Fatalf("unexpected spec %#v", got)
	}
	if _, found := got["dynamicRouting"].(map[string]any)["localOnly"]; found {
		t.Fatalf("unexpected spec %#v", got)
	}
}

func TestSubnetBuild(t *testing.T) {
	u := Subnet(Meta{Name: "s"}, SubnetSpec{Vpc: "acme", CIDR: "10.100.1.0/24", Gateway: "10.100.1.1", Provider: "default.t1-prod.fabric", ExcludeIPs: []string{"10.100.1.2..10.100.1.5"}, DHCP: true, DHCPOptions: "router=10.100.1.1,mtu=1500,dns_server=1.1.1.1;8.8.8.8", MTU: 1500})
	equal(t, spec(t, u), map[string]any{
		"vpc": "acme", "protocol": "IPv4", "cidrBlock": "10.100.1.0/24", "gateway": "10.100.1.1",
		"excludeIps": []any{"10.100.1.2..10.100.1.5"}, "provider": "default.t1-prod.fabric",
		"gatewayType": "distributed", "natOutgoing": false, "private": false, "disableGatewayCheck": true,
		"enableDHCP": true, "dhcpV4Options": "router=10.100.1.1,mtu=1500,dns_server=1.1.1.1;8.8.8.8", "mtu": int64(1500),
	})
}

func TestUnderlaySubnetBuild(t *testing.T) {
	u := Subnet(Meta{Name: "provider-public"}, SubnetSpec{CIDR: "192.0.2.0/24", Gateway: "192.0.2.1", Vlan: "public"})
	equal(t, spec(t, u), map[string]any{"protocol": "IPv4", "cidrBlock": "192.0.2.0/24", "gateway": "192.0.2.1", "vlan": "public"})
}

func TestOverlaySubnetBuild(t *testing.T) {
	u := Subnet(Meta{Name: "provider-pool"}, SubnetSpec{CIDR: "198.51.100.0/24", Gateway: "198.51.100.1"})
	equal(t, spec(t, u), map[string]any{"protocol": "IPv4", "cidrBlock": "198.51.100.0/24", "gateway": "198.51.100.1"})
}

func TestOvnEipBuildWithAndWithoutAddress(t *testing.T) {
	equal(t, spec(t, OvnEip(Meta{Name: "e"}, "provider-public", "")), map[string]any{"externalSubnet": "provider-public", "type": "nat"})
	equal(t, spec(t, OvnEip(Meta{Name: "e"}, "provider-public", "192.0.2.9")), map[string]any{"externalSubnet": "provider-public", "type": "nat", "v4Ip": "192.0.2.9"})
}

func TestOvnFipBuild(t *testing.T) {
	equal(t, spec(t, OvnFip(Meta{Name: "f"}, "e", "vm.ns.default")), map[string]any{"ovnEip": "e", "ipName": "vm.ns.default"})
}

func TestRouterLBRuleBuild(t *testing.T) {
	ports := []RouterLBPort{{Name: "tcp-80", Port: 80, TargetPort: 8080, Protocol: "TCP"}}
	with := RouterLBRule(Meta{Name: "r", Annotations: map[string]string{"a": "b"}}, "e", "acme", "ns", []string{"10.0.0.2"}, ports)
	if _, found := with.GetAnnotations()[AnnotationServiceHealthCheck]; found || with.GetAnnotations()["a"] != "b" {
		t.Fatalf("unexpected annotations %v", with.GetAnnotations())
	}
	without := RouterLBRuleWithoutHealthCheck(Meta{Name: "r"}, "e", "acme", "ns", []string{"10.0.0.2"}, ports)
	if without.GetAnnotations()[AnnotationServiceHealthCheck] != "false" {
		t.Fatalf("unexpected annotations %v", without.GetAnnotations())
	}
	equal(t, spec(t, without), map[string]any{
		"ovnEip": "e", "vpc": "acme", "namespace": "ns", "endpoints": []any{"10.0.0.2"},
		"ports": []any{map[string]any{"name": "tcp-80", "port": int64(80), "targetPort": int64(8080), "protocol": "TCP"}},
	})
}

func TestDNSZoneBuild(t *testing.T) {
	u := DNSZone(Meta{Name: "z"}, "acme", []DNSRecord{{Name: "db.internal.e2e", IPs: []string{"10.0.0.5"}}})
	equal(t, spec(t, u), map[string]any{"vpc": "acme", "records": []any{map[string]any{"name": "db.internal.e2e", "ips": []any{"10.0.0.5"}}}})
}

func TestSecurityGroupBuild(t *testing.T) {
	ingress := []SGRule{{IPVersion: "ipv4", Protocol: "tcp", RemoteType: "address", RemoteAddress: "0.0.0.0/0", Policy: "allow", Priority: 100, PortMin: 443, PortMax: 443}}
	egress := []SGRule{{IPVersion: "ipv4", Protocol: "all", RemoteType: "securityGroup", RemoteSecurityGroup: "db", Policy: "allow", Priority: 100}}
	u := SecurityGroup(Meta{Name: "g"}, ingress, egress)
	equal(t, spec(t, u), map[string]any{
		"allowSameGroupTraffic": false,
		"ingressRules":          []any{map[string]any{"ipVersion": "ipv4", "protocol": "tcp", "priority": int64(100), "remoteType": "address", "remoteAddress": "0.0.0.0/0", "policy": "allow", "portRangeMin": int64(443), "portRangeMax": int64(443)}},
		"egressRules":           []any{map[string]any{"ipVersion": "ipv4", "protocol": "all", "priority": int64(100), "remoteType": "securityGroup", "remoteSecurityGroup": "db", "policy": "allow"}},
	})
	if _, found := spec(t, SecurityGroup(Meta{Name: "g"}, nil, nil))["ingressRules"]; found {
		t.Fatal("empty rules must be omitted")
	}
}

func TestProviderNetworkBuild(t *testing.T) {
	u := ProviderNetwork(Meta{Name: "public"}, "bond0", map[string]string{LabelExternalGateway: "true"})
	equal(t, spec(t, u), map[string]any{"defaultInterface": "bond0", "nodeSelector": map[string]any{"matchLabels": map[string]any{LabelExternalGateway: "true"}}})
}

func TestVlanBuild(t *testing.T) {
	equal(t, spec(t, Vlan(Meta{Name: "public"}, 0, "public")), map[string]any{"id": int64(0), "provider": "public"})
}

func TestBgpConfBuild(t *testing.T) {
	u := BgpConf(Meta{Name: "bedrock"}, BgpConfSpec{LocalASN: 65001, RouterID: "10.0.0.9", Peers: []BgpPeer{{Address: "10.0.0.1", ASN: 65000}, {Address: "10.0.0.2", ASN: 65001}}, NodeSelector: map[string]string{LabelExternalGateway: "true"}})
	got := spec(t, u)
	if got["localASN"] != int64(65001) || got["routerId"] != "10.0.0.9" || got["peerASN"] != int64(65000) || len(got["peers"].([]any)) != 2 || !reflect.DeepEqual(got["neighbours"], []any{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("unexpected spec %#v", got)
	}
	equal(t, got["nodeSelector"].(map[string]any), map[string]any{LabelExternalGateway: "true"})
	for _, key := range []string{"holdTime", "keepaliveTime", "gracefulRestart", "advertiseFilter", "redistributeTables"} {
		if _, found := got[key]; found {
			t.Fatalf("%s must be absent: %#v", key, got)
		}
	}
	if _, found := got["peers"].([]any)[0].(map[string]any)["bfd"]; found {
		t.Fatalf("bfd must be absent: %#v", got)
	}
}

func TestBgpConfBuildTuning(t *testing.T) {
	u := BgpConf(Meta{Name: "bedrock"}, BgpConfSpec{
		LocalASN: 65001,
		Peers:    []BgpPeer{{Address: "10.0.0.1", ASN: 65000, BFD: true}, {Address: "10.0.0.2", ASN: 65001}},
		HoldTime: 9 * time.Second, KeepaliveTime: 3 * time.Second, GracefulRestart: true,
		AdvertiseFilter:    []string{"91.246.31.0/24 ge 32 le 32"},
		RedistributeTables: []uint32{198, 200},
	})
	got := spec(t, u)
	peers := got["peers"].([]any)
	equal(t, peers[0].(map[string]any), map[string]any{"address": "10.0.0.1", "asn": int64(65000), "bfd": true})
	equal(t, peers[1].(map[string]any), map[string]any{"address": "10.0.0.2", "asn": int64(65001)})
	if got["holdTime"] != "9s" || got["keepaliveTime"] != "3s" || got["gracefulRestart"] != true {
		t.Fatalf("unexpected spec %#v", got)
	}
	if !reflect.DeepEqual(got["advertiseFilter"], []any{"91.246.31.0/24 ge 32 le 32"}) || !reflect.DeepEqual(got["redistributeTables"], []any{int64(198), int64(200)}) {
		t.Fatalf("unexpected spec %#v", got)
	}
}

func TestSubnetUsedAddressesAcceptsNumberTypes(t *testing.T) {
	for _, value := range []any{int64(3), float64(3), "3"} {
		u := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"v4usingIPs": value}}}
		if got := SubnetUsedAddresses(u); got != 3 {
			t.Fatalf("%T gave %d", value, got)
		}
	}
	if SubnetUsedAddresses(&unstructured.Unstructured{Object: map[string]any{}}) != 0 {
		t.Fatal("missing count must be zero")
	}
}

func TestSubnetReadyReadsTheReadyCondition(t *testing.T) {
	ready := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	failed := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "message": "no gateway"}}}}}
	if !SubnetReady(ready) || SubnetReady(failed) || SubnetReady(&unstructured.Unstructured{Object: map[string]any{}}) {
		t.Fatal("unexpected readiness")
	}
	if SubnetErrorMessage(failed) != "no gateway" || SubnetErrorMessage(ready) != "" {
		t.Fatal("unexpected error message")
	}
}

func TestVpcStandby(t *testing.T) {
	if !VpcStandby(&unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"standby": true}}}) || VpcStandby(&unstructured.Unstructured{Object: map[string]any{}}) {
		t.Fatal("unexpected standby")
	}
}

func TestEipAddress(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"v4Ip": "192.0.2.9", "ready": true}}}
	if EipAddress(u) != "192.0.2.9" || !EipReady(u) || EipAddress(&unstructured.Unstructured{Object: map[string]any{}}) != "" {
		t.Fatal("unexpected eip")
	}
}

func TestIPOwner(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"namespace": "ns", "podName": "vm", "subnet": "s", "v4IpAddress": "10.0.0.5"}}}
	namespace, pod, subnet := IPOwner(u)
	if namespace != "ns" || pod != "vm" || subnet != "s" || IPAddress(u) != "10.0.0.5" {
		t.Fatal("unexpected owner")
	}
}

func TestVipBuild(t *testing.T) {
	equal(t, spec(t, Vip(Meta{Name: "v"}, "ns", "ns-default-0123")), map[string]any{"namespace": "ns", "subnet": "ns-default-0123", "type": "switch_lb_vip"})
}

func TestSwitchLBRuleBuild(t *testing.T) {
	ports := []RouterLBPort{{Name: "tcp-80", Port: 80, TargetPort: 8080, Protocol: "TCP"}}
	u := SwitchLBRule(Meta{Name: "r"}, "10.0.0.200", "ns", []string{"10.0.0.2"}, ports)
	equal(t, spec(t, u), map[string]any{
		"vip": "10.0.0.200", "namespace": "ns", "selector": []any{}, "endpoints": []any{"10.0.0.2"},
		"ports": []any{map[string]any{"name": "tcp-80", "port": int64(80), "targetPort": int64(8080), "protocol": "TCP"}},
	})
}

func TestVipAndSwitchRuleReaders(t *testing.T) {
	vip := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"v4ip": "10.0.0.200"}}}
	rule := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"service": "ns/slr-r"}}}
	if VipAddress(vip) != "10.0.0.200" || SwitchLBRuleService(rule) != "ns/slr-r" || VipAddress(rule) != "" {
		t.Fatal("unexpected readers")
	}
}

func TestSubnetAvailableAddressesAcceptsNumberTypes(t *testing.T) {
	for _, value := range []any{int64(7), float64(7), "7"} {
		u := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"v4availableIPs": value}}}
		if got := SubnetAvailableAddresses(u); got != 7 {
			t.Fatalf("%T gave %d", value, got)
		}
	}
}

func TestVpcReadsItsExternalSpec(t *testing.T) {
	u := Vpc(Meta{Name: "acme"}, VpcSpec{EnableExternal: true, ExtraExternalSubnets: []string{"provider-a", "provider-b"}, DefaultRoute: &StaticRoute{CIDR: "0.0.0.0/0", NextHop: "192.0.2.1"}})
	if got := VpcSpecExternalSubnets(u); !reflect.DeepEqual(got, []string{"provider-a", "provider-b"}) {
		t.Fatalf("subnets %v", got)
	}
	if got := VpcDefaultNextHop(u); got != "192.0.2.1" {
		t.Fatalf("next hop %q", got)
	}
	private := Vpc(Meta{Name: "acme"}, VpcSpec{})
	if len(VpcSpecExternalSubnets(private)) != 0 || VpcDefaultNextHop(private) != "" {
		t.Fatal("a private Vpc has no external spec")
	}
}
