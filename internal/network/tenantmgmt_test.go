package network

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var tenantRanges = ClusterRanges{Management: "10.0.10.0/24", Join: "100.64.0.0/16", Pod: "10.16.0.0/16", Service: "10.96.0.0/12"}

const transitAndServices = `[
{"name":"transit","interface":"bond0","vlan":30,"cidr":"100.65.0.0/16","gateway":"100.65.0.1","role":"transit"},
{"name":"services","interface":"bond0","vlan":31,"cidr":"100.66.0.0/15","gateway":"100.66.0.1","role":"services"},
{"name":"public","interface":"bond0","vlan":100,"cidr":"91.246.31.0/24","gateway":"91.246.31.1","role":"tenant"}]`

func enabledConfig(t *testing.T) Config {
	t.Helper()
	return configOf(t, map[string]string{"network.tenant-management.enabled": "true", "network.provider-networks": transitAndServices})
}

func aclsOf(t *testing.T, subnet *unstructured.Unstructured) []map[string]any {
	t.Helper()
	raw, _, _ := unstructured.NestedSlice(subnet.Object, "spec", "acls")
	out := make([]map[string]any, len(raw))
	for i, entry := range raw {
		out[i] = entry.(map[string]any)
	}
	return out
}

func TestTenantManagementIsOffByDefault(t *testing.T) {
	cfg := configOf(t, map[string]string{})
	if cfg.TenantManagement.Enabled || TenantManagementSubnets(cfg, tenantRanges) != nil {
		t.Fatalf("tenant management %+v", cfg.TenantManagement)
	}
	if cfg.TenantManagement.WorkloadCIDR.String() != "100.72.0.0/13" || cfg.TenantManagement.ManagedCIDR.String() != "100.80.0.0/13" {
		t.Fatalf("default ranges %+v", cfg.TenantManagement)
	}
}

func TestPlatformRangesHoldManagementJoinPodTransitAndServices(t *testing.T) {
	got := PlatformRanges(enabledConfig(t), tenantRanges)
	want := []string{"10.0.10.0/24", "10.16.0.0/16", "100.64.0.0/16", "100.65.0.0/16", "100.66.0.0/15"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("platform ranges %v", got)
	}
}

func TestPlatformRangesSkipAnEmptyManagementRange(t *testing.T) {
	got := PlatformRanges(configOf(t, map[string]string{}), ClusterRanges{Join: "100.64.0.0/16", Pod: "10.16.0.0/16"})
	if !reflect.DeepEqual(got, []string{"10.16.0.0/16", "100.64.0.0/16"}) {
		t.Fatalf("platform ranges %v", got)
	}
}

func TestTenantManagementSubnetsShape(t *testing.T) {
	subnets := TenantManagementSubnets(enabledConfig(t), tenantRanges)
	if len(subnets) != 2 {
		t.Fatalf("subnets %d", len(subnets))
	}
	want := map[string][2]string{TenantWorkloadSubnet: {"100.72.0.0/13", "100.72.0.1"}, TenantManagedSubnet: {"100.80.0.0/13", "100.80.0.1"}}
	for _, subnet := range subnets {
		spec := subnet.Object["spec"].(map[string]any)
		pair := want[subnet.GetName()]
		if spec["vpc"] != "ovn-cluster" || spec["cidrBlock"] != pair[0] || spec["gateway"] != pair[1] || spec["protocol"] != "IPv4" || spec["mtu"] != int64(1500) {
			t.Fatalf("%s spec %v", subnet.GetName(), spec)
		}
		if _, set := spec["private"]; set {
			t.Fatalf("%s must not set private", subnet.GetName())
		}
		if _, set := spec["allowSubnets"]; set {
			t.Fatalf("%s must not set allowSubnets", subnet.GetName())
		}
		labels := subnet.GetLabels()
		if labels[LabelComponent] != ComponentUnderlay || labels[LabelUsability] != UsabilityTenantManagement {
			t.Fatalf("%s labels %v", subnet.GetName(), labels)
		}
	}
}

func TestTenantWorkloadACLs(t *testing.T) {
	workload := TenantManagementSubnets(enabledConfig(t), tenantRanges)[0]
	platform := "{10.0.10.0/24, 10.16.0.0/16, 100.64.0.0/16, 100.65.0.0/16, 100.66.0.0/15}"
	want := []map[string]any{
		{"direction": "from-lport", "priority": int64(3500), "action": "drop", "match": "ct.new && ip4.dst == {10.0.10.0/24, 10.16.0.0/16, 100.64.0.0/16, 100.65.0.0/16, 100.66.0.0/15, 10.96.0.0/12}"},
		{"direction": "from-lport", "priority": int64(3500), "action": "drop", "match": "arp.op == 1 && arp.tpa != 100.72.0.1"},
		{"direction": "to-lport", "priority": int64(2500), "action": "drop", "match": "ip4.src == 100.72.0.0/13 && ip4.dst == 100.72.0.0/13 && ip4.src != 100.72.0.1 && ip4.dst != 100.72.0.1"},
		{"direction": "to-lport", "priority": int64(2500), "action": "drop", "match": "ip6"},
		{"direction": "to-lport", "priority": int64(1000), "action": "allow-related", "match": "ip4.src == " + platform},
	}
	if got := aclsOf(t, workload); !reflect.DeepEqual(got, want) {
		t.Fatalf("workload acls %v", got)
	}
}

func TestTenantManagedACLsLeaveTheServicesOpenAndDropIPv6(t *testing.T) {
	managed := TenantManagementSubnets(enabledConfig(t), tenantRanges)[1]
	platform := "{10.0.10.0/24, 10.16.0.0/16, 100.64.0.0/16, 100.65.0.0/16, 100.66.0.0/15}"
	want := []map[string]any{
		{"direction": "from-lport", "priority": int64(3500), "action": "drop", "match": "ct.new && ip4.dst == " + platform},
		{"direction": "to-lport", "priority": int64(2500), "action": "drop", "match": "ip6"},
		{"direction": "to-lport", "priority": int64(1000), "action": "allow-related", "match": "ip4.src == " + platform},
	}
	if got := aclsOf(t, managed); !reflect.DeepEqual(got, want) {
		t.Fatalf("managed acls %v", got)
	}
	for _, acl := range aclsOf(t, managed) {
		if strings.Contains(acl["match"].(string), "10.96.0.0/12") {
			t.Fatalf("managed pods must reach the services: %v", acl)
		}
	}
}

func TestTenantManagementConflict(t *testing.T) {
	cases := map[string]struct {
		values map[string]string
		bad    bool
	}{
		"defaults":                 {map[string]string{"network.provider-networks": transitAndServices}, false},
		"on the transit pool":      {map[string]string{"network.tenant-management.workload-cidr": "100.64.0.0/10", "network.provider-networks": transitAndServices}, true},
		"on the pod range":         {map[string]string{"network.tenant-management.managed-cidr": "10.16.0.0/13"}, true},
		"on the service range":     {map[string]string{"network.tenant-management.workload-cidr": "10.96.0.0/13"}, true},
		"on the management range":  {map[string]string{"network.tenant-management.workload-cidr": "10.0.8.0/22"}, true},
		"on the join range":        {map[string]string{"network.tenant-management.workload-cidr": "100.64.0.0/15"}, true},
		"on a provider network":    {map[string]string{"network.tenant-management.workload-cidr": "91.246.0.0/16", "network.provider-networks": transitAndServices}, true},
		"on the peering range":     {map[string]string{"network.tenant-management.managed-cidr": "169.254.0.0/16"}, true},
		"next to the services /15": {map[string]string{"network.tenant-management.workload-cidr": "100.68.0.0/13"}, true},
	}
	for name, c := range cases {
		values := map[string]string{}
		for k, v := range c.values {
			values[k] = v
		}
		cfg, err := ParseConfig(values)
		if err != nil {
			if !c.bad {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err := TenantManagementConflict(cfg, tenantRanges); (err != nil) != c.bad {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVPCAttachmentIsTheSharedFabricProvider(t *testing.T) {
	nad := VPCAttachment()
	spec := nad.Object["spec"].(map[string]any)
	want := `{"cniVersion":"0.3.1","provider":"vpc.kube-system.fabric","server_socket":"/run/openvswitch/fabric-daemon.sock","type":"fabric"}`
	if nad.GetNamespace() != "kube-system" || nad.GetName() != "vpc" || spec["config"] != want {
		t.Fatalf("attachment %v", nad.Object)
	}
}

func TestUnderlayReconcilerAppliesAndRemovesTenantManagement(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	t.Cleanup(func() { purgeKind(context.Background(), c, attachmentGVK) })
	withManagementCIDR(t, c)
	setSetting(t, "network.tenant-management.enabled", "true")
	reconcileUnderlay(t, c)
	got := underlayObjects(t, c)
	if got["Subnet/tenant-workload"] == nil || got["Subnet/tenant-managed"] == nil {
		t.Fatalf("objects %v", got)
	}
	nad := newObject(attachmentGVK, "vpc")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "vpc"}, nad); err != nil {
		t.Fatal(err)
	}
	setSetting(t, "network.tenant-management.enabled", "false")
	reconcileUnderlay(t, c)
	got = underlayObjects(t, c)
	if got["Subnet/tenant-workload"] != nil || got["Subnet/tenant-managed"] != nil {
		t.Fatalf("objects after disable %v", got)
	}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "vpc"}, newObject(attachmentGVK, "vpc"))
	if !apierrors.IsNotFound(err) {
		t.Fatalf("the attachment must go: %v", err)
	}
}

func TestUnderlayReconcilerKeepsAForeignVPCAttachment(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	t.Cleanup(func() { purgeKind(context.Background(), c, attachmentGVK) })
	foreign := newObject(attachmentGVK, "vpc")
	foreign.SetNamespace("kube-system")
	foreign.Object["spec"] = map[string]any{"config": "{}"}
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	reconcileUnderlay(t, c)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(foreign), newObject(attachmentGVK, "vpc")); err != nil {
		t.Fatalf("a foreign attachment must stay: %v", err)
	}
}

func TestSweeperDoesNotSweepTenantManagementSubnets(t *testing.T) {
	subnet := TenantManagementSubnets(enabledConfig(t), tenantRanges)[0]
	subnet.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-time.Hour)))
	if orphans := FindOrphans([]*unstructured.Unstructured{subnet}, nil, time.Now(), time.Minute); len(orphans) != 0 {
		t.Fatalf("orphans %v", orphans)
	}
}

func withManagementCIDR(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v1", API: v1alpha1.APISpec{VIP: "10.0.10.10"}, ManagementCIDR: "10.0.10.0/24"}}
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, cluster) })
}

func TestTenantManagementMissingNeedsTheManagementRange(t *testing.T) {
	cfg := enabledConfig(t)
	if err := TenantManagementMissing(cfg, ClusterRanges{Join: "100.64.0.0/16"}); err == nil {
		t.Fatal("an empty management range was accepted")
	}
	if err := TenantManagementMissing(cfg, tenantRanges); err != nil {
		t.Fatal(err)
	}
	if err := TenantManagementMissing(configOf(t, map[string]string{}), ClusterRanges{}); err != nil {
		t.Fatalf("a disabled setting needs nothing: %v", err)
	}
}

func TestUnderlayReconcilerRendersNothingWithoutAManagementRange(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	t.Cleanup(func() { purgeKind(context.Background(), c, attachmentGVK) })
	setSetting(t, "network.tenant-management.enabled", "true")
	reconcileUnderlay(t, c)
	if got := underlayObjects(t, c); got["Subnet/tenant-workload"] != nil || got["Subnet/tenant-managed"] != nil {
		t.Fatalf("objects %v", got)
	}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "vpc"}, newObject(attachmentGVK, "vpc"))
	if !apierrors.IsNotFound(err) {
		t.Fatalf("no attachment without the management range: %v", err)
	}
}
