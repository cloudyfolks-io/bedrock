package network

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const servicesPool = `{"name":"services","role":"services","interface":"b","vlan":20,"cidr":"100.66.0.0/15","gateway":"100.66.0.1","excludeIPs":["100.66.0.1..100.66.0.255","100.67.0.0..100.67.255.254"]}`

const servicesL2Pools = `[{"name":"public","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1"},` + servicesPool + `]`

const servicesBGPPools = `[{"name":"transit","interface":"b","vlan":30,"cidr":"100.65.0.0/16","gateway":"100.65.0.1"},{"name":"public","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1"},` + servicesPool + `]`

func servicesValues(pools string, extra map[string]string) map[string]string {
	values := map[string]string{
		"network.provider-networks":         pools,
		"network.services.provider-network": "services",
		"network.services.identity-range":   "100.67.0.0/16",
		"network.services.resolver":         "100.66.0.53",
	}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func TestParseConfigServices(t *testing.T) {
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	want := Services{ProviderNetwork: "services", IdentityRange: "100.67.0.0/16", Resolver: "100.66.0.53"}
	if cfg.Services != want {
		t.Fatalf("services %+v", cfg.Services)
	}
	if empty := configOf(t, map[string]string{}); empty.Services != (Services{}) {
		t.Fatalf("defaults %+v", empty.Services)
	}
}

func TestParseConfigRefusesBadServicesSettings(t *testing.T) {
	for _, values := range []map[string]string{
		{"network.services.provider-network": "Not A Label"},
		{"network.services.identity-range": "100.67.0.1/16"},
		{"network.services.identity-range": "100.67.0.0/31"},
		{"network.services.resolver": "resolver.example"},
	} {
		if _, err := ParseConfig(values); err == nil {
			t.Fatalf("%v must be refused", values)
		}
	}
}

func TestResolverIsTheDefaultDNSServer(t *testing.T) {
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	if !slices.Equal(cfg.DNSServers, []string{"100.66.0.53"}) {
		t.Fatalf("dns servers %v", cfg.DNSServers)
	}
	cfg = configOf(t, servicesValues(servicesL2Pools, map[string]string{"network.dns-servers": "1.1.1.1,8.8.8.8"}))
	if !slices.Equal(cfg.DNSServers, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("dns servers %v", cfg.DNSServers)
	}
}

func TestMissingServicesSettings(t *testing.T) {
	cases := map[string]struct {
		values map[string]string
		want   []string
	}{
		"not configured": {map[string]string{"network.provider-networks": servicesL2Pools}, nil},
		"complete":       {servicesValues(servicesL2Pools, nil), nil},
		"no range":       {servicesValues(servicesL2Pools, map[string]string{"network.services.identity-range": ""}), []string{"network.services.identity-range"}},
		"unknown pool":   {servicesValues(servicesL2Pools, map[string]string{"network.services.provider-network": "ghost"}), []string{"network.services.provider-network=ghost (not a provider network)"}},
		"wrong role":     {servicesValues(servicesL2Pools, map[string]string{"network.services.provider-network": "public"}), []string{"network.services.provider-network=public (its role is not services)", "network.services.identity-range=100.67.0.0/16 (not inside the cidr of the provider network)"}},
		"not excluded":   {servicesValues(`[`+strings.Replace(servicesPool, `,"excludeIPs":["100.66.0.1..100.66.0.255","100.67.0.0..100.67.255.254"]`, `,"excludeIPs":["100.67.0.0..100.67.100.0"]`, 1)+`]`, nil), []string{"network.services.identity-range=100.67.0.0/16 (not inside one excludeIPs entry of the provider network)"}},
		"range outside":  {servicesValues(servicesL2Pools, map[string]string{"network.services.identity-range": "100.70.0.0/16"}), []string{"network.services.identity-range=100.70.0.0/16 (not inside the cidr of the provider network)"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := MissingServicesSettings(configOf(t, c.values))
			if !slices.Equal(got, c.want) {
				t.Fatalf("missing %v, want %v", got, c.want)
			}
		})
	}
}

func TestServicesSubnetNeedsACompleteConfiguration(t *testing.T) {
	if subnet, ok := ServicesSubnet(configOf(t, servicesValues(servicesL2Pools, nil))); !ok || subnet != "provider-services" {
		t.Fatalf("subnet %q %v", subnet, ok)
	}
	if _, ok := ServicesSubnet(configOf(t, servicesValues(servicesL2Pools, map[string]string{"network.services.identity-range": ""}))); ok {
		t.Fatal("an incomplete configuration has no services subnet")
	}
	if _, ok := ServicesSubnet(configOf(t, map[string]string{"network.provider-networks": servicesL2Pools})); ok {
		t.Fatal("no services subnet without the Setting")
	}
}

func TestNextServicesIdentityTakesTheLowestFreeHost(t *testing.T) {
	if got, err := NextServicesIdentity("100.67.0.0/16", nil); err != nil || got != "100.67.0.1" {
		t.Fatalf("got %q %v", got, err)
	}
	if got, err := NextServicesIdentity("100.67.0.0/16", []string{"100.67.0.1", "100.67.0.3"}); err != nil || got != "100.67.0.2" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestNextServicesIdentitySkipsTheNetworkAndBroadcastAddresses(t *testing.T) {
	if got, err := NextServicesIdentity("192.0.2.0/30", []string{"192.0.2.1"}); err != nil || got != "192.0.2.2" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NextServicesIdentity("192.0.2.0/30", []string{"192.0.2.1", "192.0.2.2"}); err == nil {
		t.Fatal("a full range must fail")
	}
}

func servicesNetwork() v1alpha1.Network {
	return v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme", UID: types.UID("uid-acme")}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDR: "10.100.0.0/16", EnableExternal: true}}
}

func TestServicesPlaneObjects(t *testing.T) {
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	plane := ServicesPlane(servicesNetwork(), cfg, "100.67.0.9")
	if plane.Port.GetName() != "acme-provider-services" || plane.Port.GetKind() != "OvnEip" {
		t.Fatalf("port %v", plane.Port.Object)
	}
	spec, _, _ := unstructured.NestedMap(plane.Port.Object, "spec")
	if spec["type"] != "lrp" || spec["externalSubnet"] != "provider-services" || spec["v4Ip"] != nil {
		t.Fatalf("port spec %v", spec)
	}
	spec, _, _ = unstructured.NestedMap(plane.Identity.Object, "spec")
	if plane.Identity.GetName() != "acme-services-snat" || spec["type"] != "nat" || spec["externalSubnet"] != "provider-services" || spec["v4Ip"] != "100.67.0.9" {
		t.Fatalf("identity %v", plane.Identity.Object)
	}
	var cidrs, names []string
	for _, rule := range plane.Rules {
		spec, _, _ := unstructured.NestedMap(rule.Object, "spec")
		if spec["ovnEip"] != "acme-services-snat" || spec["vpc"] != "acme" {
			t.Fatalf("rule %v", rule.Object)
		}
		cidrs = append(cidrs, spec["v4IpCidr"].(string))
		names = append(names, rule.GetName())
	}
	if !slices.Equal(cidrs, []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}) || !slices.Equal(names, []string{"acme-services-snat-0", "acme-services-snat-1", "acme-services-snat-2"}) {
		t.Fatalf("rules %v %v", names, cidrs)
	}
	for _, object := range plane.All() {
		labels, annotations := object.GetLabels(), object.GetAnnotations()
		if labels[v1alpha1.LabelKind] != "Network" || labels[v1alpha1.LabelName] != "acme" || labels[LabelManaged] != "true" || annotations[AnnotationOwnerUID] != "uid-acme" {
			t.Fatalf("%s labels %v annotations %v", object.GetName(), labels, annotations)
		}
	}
	if !strings.HasSuffix(plane.Rules[0].GetName(), "-0") || len(plane.All()) != 5 {
		t.Fatalf("objects %d", len(plane.All()))
	}
}
