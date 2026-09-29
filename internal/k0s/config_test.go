package k0s

import (
	"reflect"
	"strings"
	"testing"

	sigyaml "sigs.k8s.io/yaml"
)

func TestRenderConfig(t *testing.T) {
	raw, err := RenderConfig(Config{VIP: "10.0.10.10", SANs: []string{"10.0.10.10", "api.lab.example"}, PodCIDR: "10.16.0.0/16", ServiceCIDR: "10.96.0.0/12"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := sigyaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc["spec"].(map[string]any)
	api := spec["api"].(map[string]any)
	if api["externalAddress"] != "10.0.10.10" || api["address"] != nil {
		t.Fatalf("api %v", api)
	}
	network := spec["network"].(map[string]any)
	if network["provider"] != "custom" || network["podCIDR"] != "10.16.0.0/16" {
		t.Fatalf("network %v", network)
	}
	if !strings.Contains(string(raw), "kind: ClusterConfig") || !strings.Contains(string(raw), "node-monitor-grace-period") {
		t.Fatalf("raw %s", raw)
	}
}

func TestRenderConfigMergesAPIExtraArgs(t *testing.T) {
	raw, err := RenderConfig(Config{VIP: "10.0.10.10", SANs: []string{"10.0.10.10"}, PodCIDR: "10.16.0.0/16", ServiceCIDR: "10.96.0.0/12", APIExtraArgs: map[string]string{"authentication-config": "/etc/bedrock/authn/authentication.yaml", "authentication-token-webhook-cache-ttl": "30s"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := sigyaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	args := doc["spec"].(map[string]any)["api"].(map[string]any)["extraArgs"].(map[string]any)
	want := map[string]any{
		"default-not-ready-toleration-seconds":   "30",
		"default-unreachable-toleration-seconds": "30",
		"authentication-config":                  "/etc/bedrock/authn/authentication.yaml",
		"authentication-token-webhook-cache-ttl": "30s",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("extraArgs %v", args)
	}
	plain, err := RenderConfig(Config{VIP: "10.0.10.10", PodCIDR: "10.16.0.0/16", ServiceCIDR: "10.96.0.0/12"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "authentication") {
		t.Fatalf("no authn flags without APIExtraArgs: %s", plain)
	}
}
