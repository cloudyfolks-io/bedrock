package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.API.VIPMode != "arp" {
		t.Fatalf("vipMode %q", cfg.Spec.API.VIPMode)
	}
	if cfg.Spec.Platform.TLSMode != "SelfSigned" {
		t.Fatalf("tlsMode %q", cfg.Spec.Platform.TLSMode)
	}
	if cfg.Spec.Network.Fabric.PodCIDR != "10.16.0.0/16" || cfg.Spec.Network.Fabric.ServiceCIDR != "10.96.0.0/12" || cfg.Spec.Network.Fabric.JoinCIDR != "100.64.0.0/16" {
		t.Fatalf("fabric cidrs %+v", cfg.Spec.Network.Fabric)
	}
	if cfg.Spec.Storage.Replicas != 1 {
		t.Fatalf("replicas %d", cfg.Spec.Storage.Replicas)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := map[string]func(*v1alpha1.ClusterConfig){
		"missing vip":      func(c *v1alpha1.ClusterConfig) { c.Spec.API.VIP = "" },
		"bad vip":          func(c *v1alpha1.ClusterConfig) { c.Spec.API.VIP = "10.0.10" },
		"bad role":         func(c *v1alpha1.ClusterConfig) { c.Spec.Roles = []string{"storage"} },
		"no control-plane": func(c *v1alpha1.ClusterConfig) { c.Spec.Roles = []string{"workload"} },
		"bad eip mode":     func(c *v1alpha1.ClusterConfig) { c.Spec.Network.Fabric.EIPMode = "arp" },
		"bad cidr":         func(c *v1alpha1.ClusterConfig) { c.Spec.Network.Fabric.PodCIDR = "10.16.0.0" },
		"custom cidr":      func(c *v1alpha1.ClusterConfig) { c.Spec.Network.Fabric.PodCIDR = "10.200.0.0/16" },
		"no version":       func(c *v1alpha1.ClusterConfig) { c.Spec.Version = "" },
		"wrong apiVersion": func(c *v1alpha1.ClusterConfig) { c.APIVersion = "bedrock.cloudyfolks.io/v1" },
		"wrong kind":       func(c *v1alpha1.ClusterConfig) { c.Kind = "Cluster" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidatePodMTU(t *testing.T) {
	cases := map[int32]bool{-1: false, 0: true, 575: false, 576: true, 1500: true, 9000: true, 9001: false}
	for mtu, valid := range cases {
		cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Spec.Network.PodMTU = mtu
		if err := Validate(cfg); (err == nil) != valid {
			t.Errorf("podMTU %d: valid %v, error %v", mtu, valid, err)
		}
	}
}

func TestLoadReadsPodMTU(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	patched := strings.Replace(string(raw), "  network:\n", "  network:\n    podMTU: 1500\n", 1)
	if patched == string(raw) {
		t.Fatal("the fixture has no network section to patch")
	}
	if err := os.WriteFile(path, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.Network.PodMTU != 1500 || ToCluster(cfg).Spec.PodMTU != 1500 {
		t.Fatalf("podMTU %d, cluster %d", cfg.Spec.Network.PodMTU, ToCluster(cfg).Spec.PodMTU)
	}
}

func TestToClusterLeavesPodMTUEmptyByDefault(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := ToCluster(cfg).Spec.PodMTU; got != 0 {
		t.Fatalf("podMTU %d", got)
	}
}

func TestValidateAllowsNoDevices(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Storage.Devices = nil
	if err := Validate(cfg); err != nil {
		t.Fatalf("no devices must be valid: %v", err)
	}
}

func TestToObjects(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cluster := ToCluster(cfg)
	if cluster.Name != v1alpha1.ClusterName || cluster.Spec.DesiredVersion != "v0.1.0" || cluster.Spec.API.VIP != "10.0.10.10" || cluster.Spec.JoinCIDR != "100.64.0.0/16" {
		t.Fatalf("cluster %+v", cluster.Spec)
	}
	host := ToHost(cfg, "node-1")
	if host.Name != "node-1" || len(host.Spec.Roles) != 4 || host.Spec.Management.Enabled || host.Spec.Storage.Devices[0] != "/dev/nvme1n1" {
		t.Fatalf("host %+v", host.Spec)
	}
	settings := ToSettings(cfg)
	want := map[string]string{"platform.tls-mode": "SelfSigned", "storage.replicas": "1", "kata.enabled": "true", "platform.host": "cloud.example.com"}
	for _, s := range settings {
		if v, ok := want[s.Name]; ok && s.Spec.Value != v {
			t.Fatalf("setting %s = %q, want %q", s.Name, s.Spec.Value, v)
		}
		delete(want, s.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing settings %v", want)
	}
}

func TestToSettingsVirtualization(t *testing.T) {
	cfg := v1alpha1.ClusterConfig{}
	cfg.Spec.Virtualization.Emulation = true
	for _, s := range ToSettings(cfg) {
		if s.Name == "virt.emulation" {
			if s.Spec.Value != "true" {
				t.Fatalf("virt.emulation = %q", s.Spec.Value)
			}
			return
		}
	}
	t.Fatal("virt.emulation setting missing")
}

func TestLoadResolvesBundleBesideConfig(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cases := []struct {
		bundle string
		want   string
	}{
		{"bundle.tar.zst", filepath.Join(dir, "bundle.tar.zst")},
		{"/srv/bedrock/bundle.tar.zst", "/srv/bedrock/bundle.tar.zst"},
		{"", ""},
	}
	for _, tc := range cases {
		cfg, err := Load(writeConfigWithBundle(t, base, dir, tc.bundle))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Spec.Registry.Bundle != tc.want {
			t.Fatalf("bundle %q resolved to %q, want %q", tc.bundle, cfg.Spec.Registry.Bundle, tc.want)
		}
	}
}

func writeConfigWithBundle(t *testing.T, base []byte, dir, bundle string) string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc["spec"].(map[string]any)
	registry, _ := spec["registry"].(map[string]any)
	next := map[string]any{"bundle": bundle}
	for key, value := range registry {
		if key != "bundle" {
			next[key] = value
		}
	}
	spec["registry"] = next
	raw, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidatePlatformHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"empty", "", "spec.platform.host is required: set the domain of the platform, for example cloud.example.com"},
		{"one label", "localhost", `spec.platform.host "localhost" is not a valid DNS name`},
		{"ip address", "192.168.5.250", `spec.platform.host "192.168.5.250" is not a valid DNS name`},
		{"upper case", "UPPER.example.com", `spec.platform.host "UPPER.example.com" is not a valid DNS name`},
		{"leading dash", "-a.example.com", `spec.platform.host "-a.example.com" is not a valid DNS name`},
		{"trailing dash", "a-.example.com", `spec.platform.host "a-.example.com" is not a valid DNS name`},
		{"empty label", "a..example.com", `spec.platform.host "a..example.com" is not a valid DNS name`},
		{"trailing dot", "example.com.", `spec.platform.host "example.com." is not a valid DNS name`},
		{"long label", strings.Repeat("a", 64) + ".example.com", `spec.platform.host "` + strings.Repeat("a", 64) + `.example.com" is not a valid DNS name`},
		{"long name", strings.Repeat("a.", 127) + "aa", `spec.platform.host "` + strings.Repeat("a.", 127) + `aa" is not a valid DNS name`},
		{"underscore", "a_b.example.com", `spec.platform.host "a_b.example.com" is not a valid DNS name`},
		{"valid", "cloud.example.com", ""},
		{"reserved tld", "e2e.bedrock.test", ""},
		{"short", "a-b.c", ""},
		{"longest label", strings.Repeat("a", 63) + ".example.com", ""},
		{"longest name", strings.Repeat("a.", 125) + "aaa", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.Spec.Platform.Host = tc.host
			err = Validate(cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Validate = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestToSettingsCarriesProviderNetworks(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range ToSettings(cfg) {
		got[s.Name] = s.Spec.Value
	}
	want := `[{"name":"public","interface":"bond0","vlan":100,"cidr":"91.246.31.0/24","gateway":"91.246.31.1"}]`
	if got["network.eip-mode"] != "l2" || got["network.provider-networks"] != want {
		t.Fatalf("settings %v", got)
	}
}

func TestToSettingsCarriesTheProviderNetworkRole(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Network.Fabric.ProviderNetworks[0].Role = v1alpha1.ProviderRolePlatform
	for _, s := range ToSettings(cfg) {
		if s.Name == "network.provider-networks" && !strings.Contains(s.Spec.Value, `"role":"platform"`) {
			t.Fatalf("value %q", s.Spec.Value)
		}
	}
}

func TestValidateRejectsAnUnknownProviderNetworkRole(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Network.Fabric.ProviderNetworks[0].Role = "public"
	if err := Validate(cfg); err == nil {
		t.Fatal("an unknown role must be refused")
	}
}

func TestToSettingsWithoutProviderNetworks(t *testing.T) {
	for _, s := range ToSettings(v1alpha1.ClusterConfig{}) {
		if s.Name == "network.provider-networks" && s.Spec.Value != "" {
			t.Fatalf("value %q", s.Spec.Value)
		}
	}
}

func TestValidateRejectsDuplicateProviderNetworkNames(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Network.Fabric.ProviderNetworks = append(cfg.Spec.Network.Fabric.ProviderNetworks, cfg.Spec.Network.Fabric.ProviderNetworks[0])
	if err := Validate(cfg); err == nil {
		t.Fatal("duplicate provider network names must be refused")
	}
}

func TestValidateRejectsExcludeIPsOutsideTheCIDR(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Network.Fabric.ProviderNetworks[0].ExcludeIPs = []string{"10.0.0.1"}
	if err := Validate(cfg); err == nil {
		t.Fatal("excludeIPs outside the cidr must be refused")
	}
}

func TestValidateKMSEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"tcp", "tcp://1.2.3.4:1", "encryption.kms.endpoint must be unix:///<absolute socket path>"},
		{"relative", "unix://run/kms.sock", "encryption.kms.endpoint must be unix:///<absolute socket path>"},
		{"parent segment", "unix:///run/../kms.sock", "encryption.kms.endpoint must be unix:///<absolute socket path>"},
		{"root only", "unix:///", "encryption.kms.endpoint must be unix:///<absolute socket path>"},
		{"valid", "unix:///run/kms/kms.sock", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.Spec.Encryption.KMS = &v1alpha1.KMSSpec{Endpoint: tc.endpoint}
			err = Validate(cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestToClusterCopiesEncryption(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Encryption.KMS = &v1alpha1.KMSSpec{Endpoint: "unix:///run/kms/kms.sock"}
	cluster := ToCluster(cfg)
	if !reflect.DeepEqual(cluster.Spec.Encryption, cfg.Spec.Encryption) {
		t.Fatalf("encryption %+v, want %+v", cluster.Spec.Encryption, cfg.Spec.Encryption)
	}
	cfg.Spec.Encryption.KMS.Endpoint = "unix:///run/kms/other.sock"
	if cluster.Spec.Encryption.KMS.Endpoint != "unix:///run/kms/kms.sock" {
		t.Fatal("ToCluster must not share the KMS spec with its input")
	}
}

func loadAdopt(t *testing.T) v1alpha1.ClusterConfig {
	t.Helper()
	cfg, err := Load(filepath.Join("testdata", "adopt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLoadReadsAdopt(t *testing.T) {
	adopt := loadAdopt(t).Spec.Storage.Adopt
	if adopt == nil {
		t.Fatal("adopt is nil")
	}
	if adopt.FSID != "2f3b1c1e-8c5e-4a54-9f41-0c2f6d7a9b10" || adopt.KeyFile != "/root/ceph-keys.yaml" {
		t.Fatalf("adopt %+v", adopt)
	}
	if len(adopt.Monitors) != 3 || adopt.Monitors[1] != (v1alpha1.AdoptMonitor{ID: "b", Node: "node-2", Address: "10.0.10.12"}) {
		t.Fatalf("monitors %+v", adopt.Monitors)
	}
	if len(adopt.Hosts) != 3 || len(adopt.Hosts[0].Devices) != 2 || adopt.Hosts[2].Node != "node-3" {
		t.Fatalf("hosts %+v", adopt.Hosts)
	}
	if adopt.Pools != (v1alpha1.AdoptPools{Block: "storage-block", Filesystem: "filesystem-data", ObjectStore: "objectstore"}) {
		t.Fatalf("pools %+v", adopt.Pools)
	}
	if adopt.Cephx == nil {
		t.Fatal("cephx is nil")
	}
	var cephx map[string]any
	if err := yaml.Unmarshal(adopt.Cephx.Raw, &cephx); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"daemon": map[string]any{"keyType": "aes256k", "keyGeneration": float64(2)}}
	if !reflect.DeepEqual(cephx, want) {
		t.Fatalf("cephx %v, want %v", cephx, want)
	}
}

func TestValidateAdopt(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.ClusterConfig)
		want   string
	}{
		{"valid", func(c *v1alpha1.ClusterConfig) {}, ""},
		{"one host needs no replica floor", func(c *v1alpha1.ClusterConfig) {
			a := c.Spec.Storage.Adopt
			a.Monitors = a.Monitors[:1]
			a.Hosts = a.Hosts[:1]
			c.Spec.Storage.Replicas = 1
		}, ""},
		{"no cephx", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Cephx = nil }, ""},
		{"empty pool names", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Pools = v1alpha1.AdoptPools{} }, ""},
		{"block pool name with a space", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Pools.Block = "storage block" }, "storage.adopt.pools.block is not a valid Ceph name"},
		{"filesystem name with a slash", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Pools.Filesystem = "filesystem/data" }, "storage.adopt.pools.filesystem is not a valid Ceph name"},
		{"object store name with shell text", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Pools.ObjectStore = "$(id)" }, "storage.adopt.pools.objectStore is not a valid Ceph name"},
		{"fsid not a uuid", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.FSID = "not-a-uuid" }, "storage.adopt.fsid: not a lowercase UUID"},
		{"fsid empty", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.FSID = "" }, "storage.adopt.fsid: not a lowercase UUID"},
		{"fsid uppercase", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.FSID = "2F3B1C1E-8C5E-4A54-9F41-0C2F6D7A9B10"
		}, "storage.adopt.fsid: not a lowercase UUID"},
		{"key file relative", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.KeyFile = "ceph-keys.yaml" }, "storage.adopt.keyFile: not an absolute path"},
		{"key file empty", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.KeyFile = "" }, "storage.adopt.keyFile: not an absolute path"},
		{"no monitors", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors = nil }, "storage.adopt.monitors: need 1, 3 or 5 monitors, got 0"},
		{"two monitors", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Monitors = c.Spec.Storage.Adopt.Monitors[:2]
		}, "storage.adopt.monitors: need 1, 3 or 5 monitors, got 2"},
		{"monitor id with a digit", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[0].ID = "a1" }, "storage.adopt.monitors[0].id: not made of lowercase letters"},
		{"monitor id empty", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[2].ID = "" }, "storage.adopt.monitors[2].id: not made of lowercase letters"},
		{"monitor id uppercase", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].ID = "B" }, "storage.adopt.monitors[1].id: not made of lowercase letters"},
		{"monitor address not an address", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Address = "10.0.10" }, "storage.adopt.monitors[1].address: not an IPv4 address"},
		{"monitor address ipv6", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Address = "fd00::12" }, "storage.adopt.monitors[1].address: not an IPv4 address"},
		{"monitor address empty", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[0].Address = "" }, "storage.adopt.monitors[0].address: not an IPv4 address"},
		{"monitor node unknown", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[2].Node = "node-9" }, `storage.adopt.monitors[2].node: "node-9" is not in storage.adopt.hosts`},
		{"device not by-id", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Hosts[0].Devices[1] = "/dev/sdb" }, "storage.adopt.hosts[0].devices[1]: not a clean path under /dev/disk/by-id/"},
		{"device by-label", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Hosts[1].Devices[0] = "/dev/disk/by-label/osd"
		}, "storage.adopt.hosts[1].devices[0]: not a clean path under /dev/disk/by-id/"},
		{"device escapes by-id", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Hosts[2].Devices[0] = "/dev/disk/by-id/../../sda"
		}, "storage.adopt.hosts[2].devices[0]: not a clean path under /dev/disk/by-id/"},
		{"device is the directory", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Hosts[2].Devices[0] = "/dev/disk/by-id/"
		}, "storage.adopt.hosts[2].devices[0]: not a clean path under /dev/disk/by-id/"},
		{"monitor id duplicate", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].ID = "a" }, `storage.adopt.monitors[1].id: duplicate "a"`},
		{"monitor node duplicate", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[2].Node = "node-1" }, `storage.adopt.monitors[2].node: duplicate "node-1"`},
		{"monitor address duplicate", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[2].Address = "10.0.10.11" }, `storage.adopt.monitors[2].address: duplicate "10.0.10.11"`},
		{"monitor address unspecified", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Address = "0.0.0.0" }, "storage.adopt.monitors[1].address: unspecified, loopback or multicast address"},
		{"monitor address loopback", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Address = "127.0.0.1" }, "storage.adopt.monitors[1].address: unspecified, loopback or multicast address"},
		{"monitor address multicast", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Address = "224.0.0.1" }, "storage.adopt.monitors[1].address: unspecified, loopback or multicast address"},
		{"monitor node uppercase", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Node = "Node-2" }, "storage.adopt.monitors[1].node: not a DNS-1123 label"},
		{"monitor node with a dot", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Monitors[1].Node = "node-2.lab" }, "storage.adopt.monitors[1].node: not a DNS-1123 label"},
		{"host node empty", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Hosts[2].Node = "" }, "storage.adopt.hosts[2].node: empty"},
		{"host node and monitor node both empty", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Hosts[2].Node = ""
			c.Spec.Storage.Adopt.Monitors[2].Node = ""
		}, "storage.adopt.hosts[2].node: empty"},
		{"host node duplicate", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Hosts[1].Node = "node-1" }, `storage.adopt.hosts[1].node: duplicate "node-1"`},
		{"host node uppercase", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Hosts[1].Node = "Node-2" }, "storage.adopt.hosts[1].node: not a DNS-1123 label"},
		{"host node with a dot", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Hosts[1].Node = "node-2.lab" }, "storage.adopt.hosts[1].node: not a DNS-1123 label"},
		{"device duplicate in one host", func(c *v1alpha1.ClusterConfig) {
			hosts := c.Spec.Storage.Adopt.Hosts
			hosts[0].Devices[1] = hosts[0].Devices[0]
		}, `storage.adopt.hosts[0].devices[1]: duplicate "/dev/disk/by-id/wwn-0x5002538b10164b70"`},
		{"device duplicate across hosts", func(c *v1alpha1.ClusterConfig) {
			hosts := c.Spec.Storage.Adopt.Hosts
			hosts[1].Devices[0] = hosts[0].Devices[0]
		}, `storage.adopt.hosts[1].devices[0]: duplicate "/dev/disk/by-id/wwn-0x5002538b10164b70"`},
		{"storage devices with adopt", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Devices = []string{"/dev/nvme1n1"} }, "storage.devices: must be empty when storage.adopt is set"},
		{"cephx a string", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`"foo"`)}
		}, "storage.adopt.cephx: not a JSON object"},
		{"cephx an array", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`[1,2]`)}
		}, "storage.adopt.cephx: not a JSON object"},
		{"cephx null", func(c *v1alpha1.ClusterConfig) {
			c.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`null`)}
		}, "storage.adopt.cephx: not a JSON object"},
		{"cephx empty", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{} }, "storage.adopt.cephx: not a JSON object"},
		{"replicas 1 with 3 hosts", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Replicas = 1 }, "storage.replicas must be at least 3 to adopt"},
		{"replicas 2 with 3 hosts", func(c *v1alpha1.ClusterConfig) { c.Spec.Storage.Replicas = 2 }, "storage.replicas must be at least 3 to adopt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadAdopt(t)
			tc.mutate(&cfg)
			err := Validate(cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Validate = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestValidateAdoptDefaultReplicasFailTheFloor(t *testing.T) {
	cfg := loadAdopt(t)
	cfg.Spec.Storage.Replicas = 0
	err := Validate(WithDefaults(cfg))
	if err == nil || err.Error() != "storage.replicas must be at least 3 to adopt" {
		t.Fatalf("Validate = %v", err)
	}
}

func TestLoadRefusesScalarCephx(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("testdata", "adopt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scalar := strings.Replace(string(base), "      cephx:\n        daemon:\n          keyType: aes256k\n          keyGeneration: 2\n", "      cephx: foo\n", 1)
	if scalar == string(base) {
		t.Fatal("testdata has no cephx block to replace")
	}
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(path, []byte(scalar), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || err.Error() != "storage.adopt.cephx: not a JSON object" {
		t.Fatalf("Load = %v", err)
	}
}

func TestToSettingsIgnoreAdopt(t *testing.T) {
	cfg := loadAdopt(t)
	plain := *cfg.DeepCopy()
	plain.Spec.Storage.Adopt = nil
	if !reflect.DeepEqual(ToSettings(cfg), ToSettings(plain)) {
		t.Fatal("ToSettings differs with adopt set")
	}
	for _, setting := range ToSettings(cfg) {
		if strings.Contains(setting.Spec.Value, "ceph-keys") || strings.Contains(setting.Name, "adopt") {
			t.Fatalf("setting %s carries adopt data", setting.Name)
		}
	}
}

func TestToClusterCopiesAdoptWithoutKeyFile(t *testing.T) {
	cfg := loadAdopt(t)
	adopt := ToCluster(cfg).Spec.Storage.Adopt
	if adopt == nil {
		t.Fatal("adopt is nil")
	}
	want := *cfg.Spec.Storage.Adopt.DeepCopy()
	want.KeyFile = ""
	if !reflect.DeepEqual(*adopt, want) {
		t.Fatalf("adopt %+v, want %+v", *adopt, want)
	}
	if cfg.Spec.Storage.Adopt.KeyFile != "/root/ceph-keys.yaml" {
		t.Fatal("ToCluster changed its input")
	}
	adopt.Hosts[0].Node = "changed"
	if cfg.Spec.Storage.Adopt.Hosts[0].Node != "node-1" {
		t.Fatal("ToCluster shares the adopt hosts with its input")
	}
}

func TestToClusterWithoutAdoptHasNone(t *testing.T) {
	cfg := loadAdopt(t)
	cfg.Spec.Storage.Adopt = nil
	if adopt := ToCluster(cfg).Spec.Storage.Adopt; adopt != nil {
		t.Fatalf("adopt %+v", adopt)
	}
}

func TestToHostTakesTheDevicesOfItsAdoptNode(t *testing.T) {
	cfg := loadAdopt(t)
	want := []string{"/dev/disk/by-id/wwn-0x5002538b10164b70", "/dev/disk/by-id/wwn-0x5002538b101652a0"}
	if got := ToHost(cfg, "node-1").Spec.Storage.Devices; !reflect.DeepEqual(got, want) {
		t.Fatalf("devices %v, want %v", got, want)
	}
	if got := ToHost(cfg, "node-9").Spec.Storage.Devices; len(got) != 0 {
		t.Fatalf("unlisted node got devices %v", got)
	}
}
