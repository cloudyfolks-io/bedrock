package v1alpha1

import (
	"maps"
	"testing"
)

func TestTenantLabelsWithProject(t *testing.T) {
	want := map[string]string{
		"bedrock.cloudyfolks.io/kind":    "VirtualMachine",
		"bedrock.cloudyfolks.io/name":    "web",
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "shop",
	}
	if got := TenantLabels("VirtualMachine", "web", "acme", "shop"); !maps.Equal(got, want) {
		t.Fatalf("labels %v", got)
	}
}

func TestTenantLabelsWithoutProject(t *testing.T) {
	got := TenantLabels("Network", "acme", "acme", "")
	if _, found := got[LabelProject]; found || len(got) != 3 {
		t.Fatalf("labels %v", got)
	}
}

func TestTenancyNames(t *testing.T) {
	if got := ProjectNamespace("acme", "shop"); got != "acme-shop" {
		t.Fatalf("namespace %q", got)
	}
	if got := TenantOwnerClusterRole("acme"); got != "tenant:acme:owner" {
		t.Fatalf("cluster role %q", got)
	}
	if got := DefaultNetworkName("acme"); got != "acme" {
		t.Fatalf("network %q", got)
	}
}
