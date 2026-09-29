package config

import (
	"os"
	"path/filepath"
	"testing"

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
	want := map[string]string{"platform.tls-mode": "SelfSigned", "storage.replicas": "1", "kata.enabled": "true", "platform.host": ""}
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
