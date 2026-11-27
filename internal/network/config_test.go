package network

import (
	"context"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const publicNetworks = `[{"name":"public","interface":"bond0","vlan":100,"cidr":"91.246.31.0/24","gateway":"91.246.31.1","excludeIPs":["91.246.31.2..91.246.31.10"]}]`

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EIPMode != "l2" || len(cfg.ProviderNetworks) != 0 || len(cfg.DNSServers) != 0 || cfg.MTU != 0 {
		t.Fatalf("unexpected defaults %+v", cfg)
	}
	if !slices.Equal(cfg.ReservedCIDRs, []string{"100.64.0.0/10"}) || !slices.Equal(cfg.SubnetAllowedRanges, []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}) {
		t.Fatalf("unexpected range defaults %v %v", cfg.ReservedCIDRs, cfg.SubnetAllowedRanges)
	}
	bgp := cfg.BGP
	if !bgp.BFD || bgp.HoldTime != 9*time.Second || bgp.KeepaliveTime != 3*time.Second || !bgp.GracefulRestart || len(bgp.AdvertiseFilter) != 0 || len(bgp.RedistributeTables) != 0 {
		t.Fatalf("unexpected bgp defaults %+v", bgp)
	}
}

func TestParseConfigLoadBalancerPlane(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil || cfg.LoadBalancer != (LoadBalancerPlane{CIDR: netip.MustParsePrefix("100.69.0.0/24"), Gateway: netip.MustParseAddr("100.69.0.1")}) {
		t.Fatalf("defaults %+v, error %v", cfg.LoadBalancer, err)
	}
	cfg, err = ParseConfig(map[string]string{"network.load-balancer.cidr": "100.70.0.0/16", "network.load-balancer.gateway": "100.70.0.254"})
	if err != nil || cfg.LoadBalancer != (LoadBalancerPlane{CIDR: netip.MustParsePrefix("100.70.0.0/16"), Gateway: netip.MustParseAddr("100.70.0.254")}) {
		t.Fatalf("custom %+v, error %v", cfg.LoadBalancer, err)
	}
}

func TestParseConfigSubnetRanges(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"network.reserved-cidrs": "100.64.0.0/10,10.16.0.0/16", "network.subnet-allowed-ranges": "10.0.0.0/8"})
	if err != nil || !slices.Equal(cfg.ReservedCIDRs, []string{"100.64.0.0/10", "10.16.0.0/16"}) || !slices.Equal(cfg.SubnetAllowedRanges, []string{"10.0.0.0/8"}) {
		t.Fatalf("reserved %v, allowed %v, error %v", cfg.ReservedCIDRs, cfg.SubnetAllowedRanges, err)
	}
}

func TestParseConfigBGPSessionSettings(t *testing.T) {
	cases := map[string]struct {
		key, value string
		check      func(BGP) bool
	}{
		"bfd off":              {"network.bgp.bfd", "false", func(b BGP) bool { return !b.BFD && b.HoldTime == 9*time.Second }},
		"hold time":            {"network.bgp.hold-time", "30s", func(b BGP) bool { return b.HoldTime == 30*time.Second && b.KeepaliveTime == 3*time.Second }},
		"keepalive time":       {"network.bgp.keepalive-time", "10s", func(b BGP) bool { return b.KeepaliveTime == 10*time.Second && b.HoldTime == 9*time.Second }},
		"graceful restart off": {"network.bgp.graceful-restart", "false", func(b BGP) bool { return !b.GracefulRestart && b.BFD }},
		"advertise filter": {"network.bgp.advertise-filter", "91.246.31.0/24 ge 32 le 32,10.0.0.0/8 ge 24 le 32", func(b BGP) bool {
			return slices.Equal(b.AdvertiseFilter, []string{"91.246.31.0/24 ge 32 le 32", "10.0.0.0/8 ge 24 le 32"})
		}},
		"redistribute tables": {"network.bgp.redistribute-tables", "198,200", func(b BGP) bool { return slices.Equal(b.RedistributeTables, []uint32{198, 200}) }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseConfig(map[string]string{tc.key: tc.value})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(cfg.BGP) {
				t.Fatalf("unexpected bgp %+v", cfg.BGP)
			}
		})
	}
}

func TestRedistributeTablesStayBelowTheVrfRange(t *testing.T) {
	last := strconv.Itoa(firstVrfID - 1)
	if _, err := ParseConfig(map[string]string{keyBGPRedistributeTables: last}); err != nil {
		t.Fatalf("table %s must be accepted: %v", last, err)
	}
	first := strconv.Itoa(firstVrfID)
	if _, err := ParseConfig(map[string]string{keyBGPRedistributeTables: first}); err == nil {
		t.Fatalf("table %s is the first VPC VRF id and must be refused", first)
	}
}

func TestEveryNetworkSettingReachesTheSettingsWatch(t *testing.T) {
	for _, key := range networkKeys {
		if !strings.HasPrefix(key, settingPrefix) {
			t.Errorf("%s is outside the %s prefix that the network watches observe", key, settingPrefix)
		}
	}
}

func TestParseConfigProviderNetworks(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"network.provider-networks": publicNetworks, "network.dns-servers": "1.1.1.1,8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	pn, ok := ProviderOf(cfg, "public")
	if !ok || pn.Interface != "bond0" || pn.Vlan != 100 || pn.ExcludeIPs[0] != "91.246.31.2..91.246.31.10" {
		t.Fatalf("unexpected provider network %+v", pn)
	}
	if _, ok := ProviderOf(cfg, "missing"); ok {
		t.Fatal("unknown provider network found")
	}
	if !slices.Equal(cfg.DNSServers, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("dns servers %v", cfg.DNSServers)
	}
}

func TestParseConfigMTU(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"network.mtu": "9000"})
	if err != nil || cfg.MTU != 9000 {
		t.Fatalf("mtu %d, error %v", cfg.MTU, err)
	}
}

func TestParseConfigBGP(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"network.eip-mode": "bgp", "network.bgp.local-asn": "65001", "network.bgp.router-id": "10.0.0.9",
		"network.bgp.peers": "10.0.0.1@65000,10.0.0.2@65001", "network.bgp.transit-network": "transit",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := BGP{LocalASN: 65001, RouterID: "10.0.0.9", Peers: []Peer{{Address: "10.0.0.1", ASN: 65000}, {Address: "10.0.0.2", ASN: 65001}}, Transit: "transit"}
	if cfg.EIPMode != "bgp" || !slices.Equal(cfg.BGP.Peers, want.Peers) || cfg.BGP.LocalASN != want.LocalASN || cfg.BGP.RouterID != want.RouterID || cfg.BGP.Transit != want.Transit {
		t.Fatalf("unexpected bgp %+v", cfg.BGP)
	}
}

func TestParseConfigRejectsInvalid(t *testing.T) {
	for _, values := range []map[string]string{
		{"network.eip-mode": "arp"},
		{"network.provider-networks": "nope"},
		{"network.bgp.peers": "10.0.0.1"},
		{"network.dns-servers": "x"},
		{"network.mtu": "575"},
		{"network.mtu": "9001"},
		{"network.mtu": "jumbo"},
		{"network.reserved-cidrs": "100.64.0.1/10"},
		{"network.subnet-allowed-ranges": "10.0.0.0/8,nonsense"},
		{"network.bgp.bfd": "maybe"},
		{"network.bgp.hold-time": "0s"},
		{"network.bgp.keepalive-time": "301s"},
		{"network.bgp.graceful-restart": "2"},
		{"network.bgp.advertise-filter": "91.246.31.0/24"},
		{"network.bgp.redistribute-tables": "0"},
		{"network.load-balancer.cidr": "100.69.0.1/24"},
		{"network.load-balancer.gateway": "nonsense"},
		{"network.load-balancer.gateway": "100.70.0.1"},
		{"network.load-balancer.cidr": "100.70.0.0/24"},
		{"network.load-balancer.cidr": "100.69.0.0/24", "network.load-balancer.gateway": "100.69.0.0"},
		{"network.load-balancer.cidr": "100.69.0.0/24", "network.load-balancer.gateway": "100.69.0.255"},
	} {
		if _, err := ParseConfig(values); err == nil {
			t.Fatalf("%v must be refused", values)
		}
	}
}

func TestMissingBGPSettings(t *testing.T) {
	cfg, _ := ParseConfig(map[string]string{"network.eip-mode": "bgp"})
	want := []string{"network.bgp.local-asn", "network.bgp.peers", "network.bgp.transit-network"}
	if got := MissingBGPSettings(cfg); !slices.Equal(got, want) {
		t.Fatalf("missing %v", got)
	}
	cfg, _ = ParseConfig(map[string]string{
		"network.eip-mode": "bgp", "network.bgp.local-asn": "65001", "network.bgp.peers": "10.0.0.1@65000",
		"network.bgp.transit-network": "ghost", "network.provider-networks": publicNetworks,
	})
	got := MissingBGPSettings(cfg)
	if len(got) != 1 || got[0] != "network.bgp.transit-network=ghost (not a provider network)" {
		t.Fatalf("missing %v", got)
	}
	cfg, _ = ParseConfig(map[string]string{
		"network.eip-mode": "bgp", "network.bgp.local-asn": "65001", "network.bgp.peers": "10.0.0.1@65000",
		"network.bgp.transit-network": "public", "network.provider-networks": publicNetworks,
	})
	if got := MissingBGPSettings(cfg); len(got) != 0 {
		t.Fatalf("missing %v", got)
	}
}

func TestLoadConfigOverlaysSettingsOnDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.eip-mode"}, Spec: v1alpha1.SettingSpec{Value: "bgp"}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.dns-servers"}, Spec: v1alpha1.SettingSpec{Value: "9.9.9.9"}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.host"}, Spec: v1alpha1.SettingSpec{Value: "cloud.example.com"}},
	).Build()
	cfg, err := LoadConfig(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EIPMode != "bgp" || !slices.Equal(cfg.DNSServers, []string{"9.9.9.9"}) {
		t.Fatalf("unexpected config %+v", cfg)
	}
}
