package operator

import (
	"context"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/config"
)

func TestTenantLegsRejectAnyAddressSource(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	legs := func(source string) []v1alpha1.TenantLegSpec {
		return []v1alpha1.TenantLegSpec{{Interface: "br-public.30", AllowSources: []string{source}}}
	}
	for _, source := range []string{"0.0.0.0/0", "::/0"} {
		cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "legs-cluster"}, Spec: v1alpha1.ClusterSpec{TenantLegs: legs(source)}}
		if err := c.Create(ctx, cluster); err == nil {
			t.Fatalf("Cluster must reject %s", source)
		}
		hostConfig := &v1alpha1.HostConfig{ObjectMeta: metav1.ObjectMeta{Name: "legs-host"}, Spec: v1alpha1.HostConfigSpec{TenantLegs: legs(source)}}
		if err := c.Create(ctx, hostConfig); err == nil {
			t.Fatalf("HostConfig must reject %s", source)
		}
	}
	ok := &v1alpha1.HostConfig{ObjectMeta: metav1.ObjectMeta{Name: "legs-host"}, Spec: v1alpha1.HostConfigSpec{TenantLegs: legs("100.65.0.1/32")}}
	if err := c.Create(ctx, ok); err != nil {
		t.Fatal(err)
	}
}

func TestClusterConfigValidatesTheUnderlayFields(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	base, err := config.Load(filepath.Join("..", "config", "testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*v1alpha1.ClusterConfig){
		"management cidr": func(cfg *v1alpha1.ClusterConfig) { cfg.Spec.Network.ManagementCIDR = "10.0.10.1" },
		"leg source": func(cfg *v1alpha1.ClusterConfig) {
			cfg.Spec.Network.TenantLegs = []v1alpha1.TenantLegSpec{{Interface: "leg0", AllowSources: []string{"nope"}}}
		},
		"any source": func(cfg *v1alpha1.ClusterConfig) {
			cfg.Spec.Network.TenantLegs = []v1alpha1.TenantLegSpec{{Interface: "leg0", AllowSources: []string{"0.0.0.0/0"}}}
		},
		"duplicate leg": func(cfg *v1alpha1.ClusterConfig) {
			cfg.Spec.Network.TenantLegs = []v1alpha1.TenantLegSpec{{Interface: "leg0"}, {Interface: "leg0"}}
		},
		"leg name": func(cfg *v1alpha1.ClusterConfig) {
			cfg.Spec.Network.TenantLegs = []v1alpha1.TenantLegSpec{{Interface: "a b"}}
		},
	}
	for name, mutate := range cases {
		cfg := base.DeepCopy()
		cfg.ObjectMeta = metav1.ObjectMeta{Name: "cfg"}
		mutate(cfg)
		if err := c.Create(ctx, cfg); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	good := base.DeepCopy()
	good.ObjectMeta = metav1.ObjectMeta{Name: "cfg"}
	good.Spec.Network.ManagementCIDR = "10.0.10.0/24"
	good.Spec.Network.TenantLegs = []v1alpha1.TenantLegSpec{{Interface: "br-public.30", AllowSources: []string{"100.65.0.1/32"}}}
	if err := c.Create(ctx, good); err != nil {
		t.Fatal(err)
	}
}
