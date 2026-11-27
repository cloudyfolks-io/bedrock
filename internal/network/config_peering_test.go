package network

import "testing"

func TestParseConfigPeeringRange(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil || cfg.PeeringRange != "169.254.0.0/16" {
		t.Fatalf("default %q, error %v", cfg.PeeringRange, err)
	}
	cfg, err = ParseConfig(map[string]string{"network.peering.range": "10.77.0.0/24"})
	if err != nil || cfg.PeeringRange != "10.77.0.0/24" {
		t.Fatalf("range %q, error %v", cfg.PeeringRange, err)
	}
	if _, err := ParseConfig(map[string]string{"network.peering.range": "10.77.0.0/31"}); err == nil {
		t.Fatal("a range smaller than a /30 must be refused")
	}
}
