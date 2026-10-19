package network

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func configOf(t *testing.T, values map[string]string) Config {
	t.Helper()
	cfg, err := ParseConfig(values)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func byKindAndName(objects []*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	out := map[string]*unstructured.Unstructured{}
	for _, o := range objects {
		out[o.GetKind()+"/"+o.GetName()] = o
	}
	return out
}

func nestedString(t *testing.T, u *unstructured.Unstructured, path ...string) string {
	t.Helper()
	value, _, _ := unstructured.NestedString(u.Object, path...)
	return value
}

func TestRenderUnderlayL2(t *testing.T) {
	cfg := configOf(t, map[string]string{"network.provider-networks": publicNetworks})
	objects, warnings := RenderUnderlay(cfg)
	if len(warnings) != 0 || len(objects) != 3 {
		t.Fatalf("objects %d warnings %v", len(objects), warnings)
	}
	got := byKindAndName(objects)
	pn, vlan, subnet := got["ProviderNetwork/public"], got["Vlan/public"], got["Subnet/provider-public"]
	if pn == nil || vlan == nil || subnet == nil {
		t.Fatalf("missing objects: %v", slices.Collect(func(yield func(string) bool) {
			for k := range got {
				if !yield(k) {
					return
				}
			}
		}))
	}
	if nestedString(t, pn, "spec", "defaultInterface") != "bond0" {
		t.Fatal("interface")
	}
	if id, _, _ := unstructured.NestedInt64(vlan.Object, "spec", "id"); id != 100 || nestedString(t, vlan, "spec", "provider") != "public" {
		t.Fatalf("vlan %v", vlan.Object["spec"])
	}
	if nestedString(t, subnet, "spec", "vlan") != "public" || nestedString(t, subnet, "spec", "cidrBlock") != "91.246.31.0/24" || nestedString(t, subnet, "spec", "gateway") != "91.246.31.1" {
		t.Fatalf("subnet %v", subnet.Object["spec"])
	}
	for _, o := range objects {
		labels := o.GetLabels()
		if labels[LabelComponent] != ComponentUnderlay || labels[LabelManaged] != "true" || labels[v1alpha1.LabelKind] != o.GetKind() || labels[v1alpha1.LabelName] != o.GetName() {
			t.Fatalf("labels of %s: %v", o.GetKind(), labels)
		}
	}
}

func TestRenderUnderlayLabelsPoolsByRole(t *testing.T) {
	roles := map[string]string{"": "public", "tenant": "public", "platform": "private", "transit": "transit", "services": "services"}
	for role, want := range roles {
		pn := `{"name":"pool","interface":"bond0","cidr":"198.51.100.0/24","gateway":"198.51.100.1"`
		if role != "" {
			pn += `,"role":"` + role + `"`
		}
		objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": "[" + pn + "}]"}))
		subnet := byKindAndName(objects)["Subnet/provider-pool"]
		if got := subnet.GetLabels()["usability"]; got != want {
			t.Fatalf("role %q: usability %q, want %q", role, got, want)
		}
	}
}

func TestRenderUnderlayBGPLabelsTheTransitPoolAsTransit(t *testing.T) {
	for _, role := range []string{"", "tenant", "platform"} {
		pools := twoNetworks
		if role != "" {
			pools = strings.Replace(pools, `"gateway":"192.0.2.1"`, `"gateway":"192.0.2.1","role":"`+role+`"`, 1)
		}
		objects, _ := RenderUnderlay(configOf(t, bgpValues(map[string]string{"network.provider-networks": pools})))
		got := byKindAndName(objects)
		if label := got["Subnet/provider-transit"].GetLabels()["usability"]; label != "transit" {
			t.Fatalf("role %q: transit pool usability %q", role, label)
		}
		if label := got["Subnet/provider-pool"].GetLabels()["usability"]; label != "public" {
			t.Fatalf("role %q: tenant pool usability %q", role, label)
		}
	}
}

func TestRenderUnderlayL2KeepsTheRoleOfAPoolNamedLikeTheTransit(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": twoNetworks, "network.bgp.transit-network": "transit"}))
	if label := byKindAndName(objects)["Subnet/provider-transit"].GetLabels()["usability"]; label != "public" {
		t.Fatalf("usability %q", label)
	}
}

func TestRenderUnderlayLabelsOnlyTheSubnetWithUsability(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": publicNetworks}))
	for _, o := range objects {
		if _, found := o.GetLabels()["usability"]; found != (o.GetKind() == "Subnet") {
			t.Fatalf("%s has the usability label: %v", o.GetKind(), found)
		}
	}
}

func TestRenderUnderlayVlanZero(t *testing.T) {
	cfg := configOf(t, map[string]string{"network.provider-networks": `[{"name":"flat","interface":"eth1","cidr":"192.0.2.0/24","gateway":"192.0.2.1"}]`})
	objects, _ := RenderUnderlay(cfg)
	vlan := byKindAndName(objects)["Vlan/flat"]
	id, found, _ := unstructured.NestedInt64(vlan.Object, "spec", "id")
	if !found || id != 0 {
		t.Fatalf("vlan id %d found %v", id, found)
	}
}

func TestRenderUnderlayExcludeIPs(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": publicNetworks}))
	excludes, _, _ := unstructured.NestedStringSlice(byKindAndName(objects)["Subnet/provider-public"].Object, "spec", "excludeIps")
	if !slices.Equal(excludes, []string{"91.246.31.2..91.246.31.10"}) {
		t.Fatalf("excludeIps %v", excludes)
	}
}

func TestRenderUnderlayNoNetworks(t *testing.T) {
	objects, warnings := RenderUnderlay(configOf(t, map[string]string{}))
	if len(objects) != 0 || len(warnings) != 0 {
		t.Fatalf("objects %d warnings %v", len(objects), warnings)
	}
}

const twoNetworks = `[{"name":"transit","interface":"bond0","vlan":10,"cidr":"192.0.2.0/24","gateway":"192.0.2.1"},{"name":"pool","interface":"bond0","vlan":20,"cidr":"198.51.100.0/24","gateway":"198.51.100.1"}]`

func bgpValues(extra map[string]string) map[string]string {
	values := map[string]string{
		"network.eip-mode": "bgp", "network.provider-networks": twoNetworks, "network.bgp.local-asn": "65001",
		"network.bgp.peers": "10.0.0.1@65000,10.0.0.2@65001", "network.bgp.transit-network": "transit", "network.bgp.router-id": "10.0.0.9",
	}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func TestRenderUnderlayBGPSplitsTransitAndPools(t *testing.T) {
	objects, warnings := RenderUnderlay(configOf(t, bgpValues(nil)))
	if len(warnings) != 0 {
		t.Fatalf("warnings %v", warnings)
	}
	got := byKindAndName(objects)
	for _, want := range []string{"ProviderNetwork/transit", "Vlan/transit", "Subnet/provider-transit", "Subnet/provider-pool", "BgpConf/bedrock"} {
		if got[want] == nil {
			t.Fatalf("missing %s", want)
		}
	}
	if got["ProviderNetwork/pool"] != nil || got["Vlan/pool"] != nil {
		t.Fatal("a pool network has no provider network and no vlan")
	}
	if _, found, _ := unstructured.NestedString(got["Subnet/provider-pool"].Object, "spec", "vlan"); found {
		t.Fatal("a pool Subnet has no vlan")
	}
	if nestedString(t, got["Subnet/provider-transit"], "spec", "vlan") != "transit" {
		t.Fatal("the transit Subnet needs its vlan")
	}
}

func TestRenderUnderlayBGPConf(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, bgpValues(nil)))
	spec, _, _ := unstructured.NestedMap(byKindAndName(objects)["BgpConf/bedrock"].Object, "spec")
	peers, _ := spec["peers"].([]any)
	neighbours, _ := spec["neighbours"].([]any)
	if len(peers) != 2 || len(neighbours) != 2 || spec["peerASN"] != int64(65000) || spec["localASN"] != int64(65001) || spec["routerId"] != "10.0.0.9" {
		t.Fatalf("spec %v", spec)
	}
}

func bgpConfSpecOf(t *testing.T, values map[string]string) map[string]any {
	t.Helper()
	objects, warnings := RenderUnderlay(configOf(t, bgpValues(values)))
	if len(warnings) != 0 {
		t.Fatalf("warnings %v", warnings)
	}
	spec, _, _ := unstructured.NestedMap(byKindAndName(objects)["BgpConf/bedrock"].Object, "spec")
	return spec
}

func peerBFD(spec map[string]any) []any {
	peers, _ := spec["peers"].([]any)
	flags := make([]any, len(peers))
	for i, peer := range peers {
		flags[i] = peer.(map[string]any)["bfd"]
	}
	return flags
}

func TestRenderUnderlayBGPConfSessionDefaults(t *testing.T) {
	spec := bgpConfSpecOf(t, nil)
	if !reflect.DeepEqual(peerBFD(spec), []any{true, true}) || spec["holdTime"] != "9s" || spec["keepaliveTime"] != "3s" || spec["gracefulRestart"] != true {
		t.Fatalf("spec %v", spec)
	}
	if _, found := spec["redistributeTables"]; found {
		t.Fatalf("redistributeTables must be absent by default: %v", spec)
	}
}

func advertisedFilter(t *testing.T, spec map[string]any) []string {
	t.Helper()
	items, _ := spec["advertiseFilter"].([]any)
	filter := make([]string, len(items))
	for i, item := range items {
		filter[i] = item.(string)
	}
	return filter
}

func TestRenderUnderlayBGPConfDeniesTheLoadBalancerPlaneWithAnEmptyFilter(t *testing.T) {
	filter := advertisedFilter(t, bgpConfSpecOf(t, nil))
	for route, want := range map[string]bool{"100.69.0.0/24": false, "100.69.0.5/32": false, "100.69.0.128/25": false, "100.69.1.5/32": true, "10.20.0.0/16": true, "91.246.31.20/32": true} {
		if got := filterMatches(t, filter, netip.MustParsePrefix(route)); got != want {
			t.Fatalf("%s advertised=%v want %v in %v", route, got, want, filter)
		}
	}
}

func TestRenderUnderlayBGPConfDeniesTheLoadBalancerPlaneWithACustomFilter(t *testing.T) {
	filter := advertisedFilter(t, bgpConfSpecOf(t, map[string]string{"network.bgp.advertise-filter": "100.64.0.0/10 ge 32 le 32,91.246.31.0/24 ge 32 le 32"}))
	for route, want := range map[string]bool{"100.69.0.5/32": false, "100.69.0.200/32": false, "100.69.1.5/32": true, "100.70.0.9/32": true, "91.246.31.20/32": true, "10.20.0.0/16": false} {
		if got := filterMatches(t, filter, netip.MustParsePrefix(route)); got != want {
			t.Fatalf("%s advertised=%v want %v in %v", route, got, want, filter)
		}
	}
}

func TestRenderUnderlayBGPConfDeniesTheConfiguredLoadBalancerPlane(t *testing.T) {
	filter := advertisedFilter(t, bgpConfSpecOf(t, map[string]string{"network.load-balancer.cidr": "100.70.0.0/16", "network.load-balancer.gateway": "100.70.0.1"}))
	for route, want := range map[string]bool{"100.70.3.5/32": false, "100.69.0.5/32": true} {
		if got := filterMatches(t, filter, netip.MustParsePrefix(route)); got != want {
			t.Fatalf("%s advertised=%v want %v in %v", route, got, want, filter)
		}
	}
}

func TestRenderUnderlayBGPConfFollowsEachSessionSetting(t *testing.T) {
	cases := map[string]struct {
		key, value string
		check      func(map[string]any) bool
	}{
		"bfd off": {"network.bgp.bfd", "false", func(s map[string]any) bool {
			return reflect.DeepEqual(peerBFD(s), []any{nil, nil}) && s["holdTime"] == "9s"
		}},
		"hold time":      {"network.bgp.hold-time", "30s", func(s map[string]any) bool { return s["holdTime"] == "30s" && s["keepaliveTime"] == "3s" }},
		"keepalive time": {"network.bgp.keepalive-time", "10s", func(s map[string]any) bool { return s["keepaliveTime"] == "10s" && s["holdTime"] == "9s" }},
		"graceful restart off": {"network.bgp.graceful-restart", "false", func(s map[string]any) bool {
			_, found := s["gracefulRestart"]
			return !found && s["holdTime"] == "9s"
		}},
		"advertise filter": {"network.bgp.advertise-filter", "91.246.31.0/24 ge 32 le 32", func(s map[string]any) bool {
			return reflect.DeepEqual(s["advertiseFilter"], []any{"91.246.31.0/24 ge 32 le 32"})
		}},
		"redistribute tables": {"network.bgp.redistribute-tables", "198,200", func(s map[string]any) bool {
			return reflect.DeepEqual(s["redistributeTables"], []any{int64(198), int64(200)})
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := bgpConfSpecOf(t, map[string]string{tc.key: tc.value})
			if !tc.check(spec) {
				t.Fatalf("spec %v", spec)
			}
		})
	}
}

func TestRenderUnderlayBGPIncompleteRendersNoBgpConf(t *testing.T) {
	objects, warnings := RenderUnderlay(configOf(t, bgpValues(map[string]string{"network.bgp.local-asn": ""})))
	if byKindAndName(objects)["BgpConf/bedrock"] != nil || byKindAndName(objects)["Subnet/provider-transit"] == nil {
		t.Fatal("the Subnets stay and the BgpConf is skipped")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "network.bgp.local-asn") {
		t.Fatalf("warnings %v", warnings)
	}
}

func TestRenderUnderlayBGPUnknownTransit(t *testing.T) {
	objects, warnings := RenderUnderlay(configOf(t, bgpValues(map[string]string{"network.bgp.transit-network": "ghost"})))
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ghost") {
		t.Fatalf("warnings %v", warnings)
	}
	if byKindAndName(objects)["BgpConf/bedrock"] != nil {
		t.Fatal("no BgpConf without a transit network")
	}
}
