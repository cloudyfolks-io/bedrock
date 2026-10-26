package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const transitPoolSetting = `[{"name":"transit","interface":"bond0","vlan":30,"role":"transit","cidr":"100.65.0.0/16","gateway":"100.65.0.1"}]`

func upgradeWithNetwork(t *testing.T, pools, managementCIDR string, legs []v1alpha1.TenantLegSpec) string {
	t.Helper()
	withVersion(t, "v0.3.0")
	c := upgradeClient(t, "v0.2.0", "amd64")
	ctx := context.Background()
	if pools != "" {
		if err := c.Create(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.provider-networks"}, Spec: v1alpha1.SettingSpec{Value: pools}}); err != nil {
			t.Fatal(err)
		}
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Spec.ManagementCIDR = managementCIDR
	cluster.Spec.TenantLegs = legs
	if err := c.Update(ctx, &cluster); err != nil {
		t.Fatal(err)
	}
	go playOperator(t, c)
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(ctx, upgradeOptions{to: "v0.3.0", bundles: []string{writeUpgradeBundle(t, "v0.3.0", "amd64")}, root: t.TempDir(), timeout: 10 * time.Second}, upgradeDeps(c, ""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("a warning must not block: exit %d stderr %q", code, stderr.String())
	}
	return stderr.String()
}

func TestUpgradeWarnsWhenTheNetworkScopeIsUnset(t *testing.T) {
	got := upgradeWithNetwork(t, transitPoolSetting, "", nil)
	for _, want := range []string{"warning", "managementCIDR", "tenantLegs"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr %q lacks %q", got, want)
		}
	}
}

func TestUpgradeWarnsForTheFieldThatIsUnset(t *testing.T) {
	got := upgradeWithNetwork(t, transitPoolSetting, "10.0.10.0/24", nil)
	if !strings.Contains(got, "tenantLegs") || strings.Contains(got, "managementCIDR") {
		t.Fatalf("stderr %q", got)
	}
}

func TestUpgradeIsQuietWhenTheNetworkScopeIsSet(t *testing.T) {
	legs := []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}}
	if got := upgradeWithNetwork(t, transitPoolSetting, "10.0.10.0/24", legs); strings.Contains(got, "warning") {
		t.Fatalf("stderr %q", got)
	}
}

func TestUpgradeIsQuietWithoutTransitOrServicesPools(t *testing.T) {
	if got := upgradeWithNetwork(t, "", "", nil); strings.Contains(got, "warning") {
		t.Fatalf("stderr %q", got)
	}
}
