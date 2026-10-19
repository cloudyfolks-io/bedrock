package clusters

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func resolveCluster(t *testing.T, h *harness, cluster *v1alpha1.KubernetesCluster) (Resolved, *Gate) {
	t.Helper()
	resolved, gate, err := h.r.resolver().Resolve(context.Background(), *cluster)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, gate
}

func requireGate(t *testing.T, gate *Gate, reason string) {
	t.Helper()
	if gate == nil || gate.Reason != reason {
		t.Fatalf("gate %+v, want %s", gate, reason)
	}
	if gate.Message == "" {
		t.Fatal("a gate needs a message")
	}
}

func TestResolveSucceedsWithTheFixture(t *testing.T) {
	h := newHarness(t)
	resolved, gate := resolveCluster(t, h, testCluster2("prod"))
	if gate != nil {
		t.Fatalf("gate %+v", gate)
	}
	if resolved.Address != "10.100.0.10" || !resolved.AddressIsNew {
		t.Fatalf("resolved %+v", resolved)
	}
}

func TestResolveKeepsTheRecordedAddress(t *testing.T) {
	h := newHarness(t)
	cluster := testCluster2("prod")
	cluster.Status.ControlPlane.Address = "10.100.0.77"
	resolved, gate := resolveCluster(t, h, cluster)
	if gate != nil || resolved.Address != "10.100.0.77" || resolved.AddressIsNew {
		t.Fatalf("resolved %+v gate %+v", resolved, gate)
	}
}

func TestResolveAddonDisabled(t *testing.T) {
	cases := map[string]func(*harness){
		"addon object missing": func(h *harness) {
			addon := &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "clusters"}}
			if err := h.client.Delete(context.Background(), addon); err != nil {
				t.Fatal(err)
			}
		},
		"addon off": func(h *harness) {
			var addon v1alpha1.Addon
			if err := h.client.Get(context.Background(), client.ObjectKey{Name: "clusters"}, &addon); err != nil {
				t.Fatal(err)
			}
			addon.Spec.Enabled = false
			if err := h.client.Update(context.Background(), &addon); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			change(h)
			_, gate := resolveCluster(t, h, testCluster2("prod"))
			requireGate(t, gate, ReasonAddonDisabled)
		})
	}
}

func TestResolveAddonKindsNotServed(t *testing.T) {
	h := newHarness(t)
	noKind := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "k0smotron.io", Kind: "Cluster"}, SearchedVersions: []string{"v1beta2"}}
	h.client = fake.NewClientBuilder().WithScheme(fixtureScheme(t)).WithObjects(fixtureObjects()...).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if list.GetObjectKind().GroupVersionKind() == clusterListGVK {
				return noKind
			}
			return c.List(ctx, list, opts...)
		},
	}).Build()
	h.r = h.newReconciler()
	_, gate := resolveCluster(t, h, testCluster2("prod"))
	requireGate(t, gate, ReasonAddonDisabled)
}

func TestResolveSubnetGates(t *testing.T) {
	cluster := testCluster2("prod")
	t.Run("missing", func(t *testing.T) {
		h := newHarness(t)
		other := testCluster2("prod")
		other.Spec.Network.SubnetRef = "nowhere"
		_, gate := resolveCluster(t, h, other)
		requireGate(t, gate, ReasonSubnetNotFound)
	})
	t.Run("not ready", func(t *testing.T) {
		h := newHarness(t)
		var subnet v1alpha1.Subnet
		_ = h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: "default"}, &subnet)
		subnet.Status.Conditions = nil
		if err := h.client.Status().Update(context.Background(), &subnet); err != nil {
			t.Fatal(err)
		}
		_, gate := resolveCluster(t, h, cluster)
		requireGate(t, gate, ReasonSubnetNotReady)
	})
	t.Run("network without a vpc", func(t *testing.T) {
		h := newHarness(t)
		var parent v1alpha1.Network
		_ = h.client.Get(context.Background(), client.ObjectKey{Name: fixtureNetwork}, &parent)
		parent.Status.VPC = ""
		if err := h.client.Status().Update(context.Background(), &parent); err != nil {
			t.Fatal(err)
		}
		_, gate := resolveCluster(t, h, cluster)
		requireGate(t, gate, ReasonSubnetNotReady)
	})
}

func TestResolveNoExternalPath(t *testing.T) {
	t.Run("external off", func(t *testing.T) {
		h := newHarness(t)
		var parent v1alpha1.Network
		_ = h.client.Get(context.Background(), client.ObjectKey{Name: fixtureNetwork}, &parent)
		parent.Spec.EnableExternal = false
		if err := h.client.Update(context.Background(), &parent); err != nil {
			t.Fatal(err)
		}
		_, gate := resolveCluster(t, h, testCluster2("prod"))
		requireGate(t, gate, ReasonNoExternalPath)
	})
	t.Run("external not connected", func(t *testing.T) {
		h := newHarness(t)
		var parent v1alpha1.Network
		_ = h.client.Get(context.Background(), client.ObjectKey{Name: fixtureNetwork}, &parent)
		parent.Status.Conditions = nil
		if err := h.client.Status().Update(context.Background(), &parent); err != nil {
			t.Fatal(err)
		}
		_, gate := resolveCluster(t, h, testCluster2("prod"))
		requireGate(t, gate, ReasonNoExternalPath)
	})
}

func TestResolveSecurityGroups(t *testing.T) {
	t.Run("control plane group missing", func(t *testing.T) {
		h := newHarness(t)
		cluster := testCluster2("prod")
		cluster.Spec.ControlPlane.SecurityGroups = []string{"absent"}
		_, gate := resolveCluster(t, h, cluster)
		requireGate(t, gate, ReasonSecurityGroupNotReady)
	})
	t.Run("worker group not ready", func(t *testing.T) {
		h := newHarness(t)
		var group v1alpha1.SecurityGroup
		_ = h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: "extra"}, &group)
		group.Status.Conditions = nil
		if err := h.client.Status().Update(context.Background(), &group); err != nil {
			t.Fatal(err)
		}
		_, gate := resolveCluster(t, h, testCluster2("prod"))
		requireGate(t, gate, ReasonSecurityGroupNotReady)
	})
}

func TestResolveCIDRs(t *testing.T) {
	cases := []struct {
		name   string
		pod    string
		svc    string
		extra  []client.Object
		reason string
	}{
		{"pod and service overlap", "100.64.0.0/16", "100.64.128.0/17", nil, ReasonCIDROverlap},
		{"pod overlaps the subnet", "10.100.0.0/24", "100.65.0.0/16", nil, ReasonCIDROverlap},
		{"service overlaps a subnet of the same network", "100.64.0.0/16", "10.200.0.0/16", []client.Object{subnetOf("other", fixtureNetwork, "10.200.0.0/20")}, ReasonCIDROverlap},
		{"a subnet of another network is no clash", "100.64.0.0/16", "10.200.0.0/16", []client.Object{subnetOf("other", "beta", "10.200.0.0/20")}, ""},
		{"disjoint ranges", "100.64.0.0/16", "100.65.0.0/16", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.extra...)
			cluster := testCluster2("prod")
			cluster.Spec.Network.PodCIDR, cluster.Spec.Network.ServiceCIDR = tc.pod, tc.svc
			_, gate := resolveCluster(t, h, cluster)
			if tc.reason == "" {
				if gate != nil {
					t.Fatalf("gate %+v", gate)
				}
				return
			}
			requireGate(t, gate, tc.reason)
		})
	}
}

func subnetOf(name, networkRef, cidr string) *v1alpha1.Subnet {
	return &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: name}, Spec: v1alpha1.SubnetSpec{NetworkRef: networkRef, CIDR: cidr}}
}

func TestResolveCIDRAgainstThePlatformPlanes(t *testing.T) {
	for _, key := range []string{"network.load-balancer.cidr", "network.services.identity-range"} {
		t.Run(key, func(t *testing.T) {
			h := newHarness(t, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}, Spec: v1alpha1.SettingSpec{Value: "100.65.4.0/24"}})
			_, gate := resolveCluster(t, h, testCluster2("prod"))
			requireGate(t, gate, ReasonCIDROverlap)
		})
	}
}

func TestResolveIgnoresTheReservedRangeSettingForTheDefaultCIDRs(t *testing.T) {
	h := newHarness(t, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.reserved-cidrs"}, Spec: v1alpha1.SettingSpec{Value: "100.64.0.0/10"}})
	if _, gate := resolveCluster(t, h, testCluster2("prod")); gate != nil {
		t.Fatalf("the default CIDRs of the cluster live inside k0s and need no platform range: %+v", gate)
	}
}

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name    string
		setting string
		version string
		reason  string
	}{
		{"default list holds the default version", "", "v1.36.3+k0s.1", ""},
		{"default list refuses another version", "", "v1.35.0+k0s.0", ReasonVersionNotAllowed},
		{"explicit list", "v1.35.0+k0s.0 v1.36.3+k0s.1", "v1.35.0+k0s.0", ""},
		{"any allows every matching version", "any", "v1.30.0+k0s.2", ""},
		{"broken setting", "latest", "v1.36.3+k0s.1", ReasonVersionNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var objects []client.Object
			if tc.setting != "" {
				objects = append(objects, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "clusters.versions"}, Spec: v1alpha1.SettingSpec{Value: tc.setting}})
			}
			h := newHarness(t, objects...)
			cluster := testCluster2("prod")
			cluster.Spec.Version = tc.version
			_, gate := resolveCluster(t, h, cluster)
			if tc.reason == "" {
				if gate != nil {
					t.Fatalf("gate %+v", gate)
				}
				return
			}
			requireGate(t, gate, tc.reason)
		})
	}
}

func TestResolveAddressGates(t *testing.T) {
	subnetLabel := network.FabricName(fixtureNamespace, "default")
	cases := []struct {
		name    string
		pinned  string
		objects []client.Object
		reason  string
		address string
	}{
		{"pinned and free", "10.100.0.50", nil, "", "10.100.0.50"},
		{"pinned outside the subnet", "10.200.0.50", nil, ReasonAddressOutsideSubnet, ""},
		{"pinned gateway", "10.100.0.1", nil, ReasonAddressReserved, ""},
		{"pinned broadcast", "10.100.0.255", nil, ReasonAddressReserved, ""},
		{"pinned and held by a pod", "10.100.0.50", []client.Object{podFabricIP("ip1", subnetLabel, "10.100.0.50", fixtureNamespace, "other-pod")}, ReasonAddressInUse, ""},
		{"pinned and held by the status of another cluster", "10.100.0.50", []client.Object{statusCluster("other", "10.100.0.50")}, ReasonAddressInUse, ""},
		{"pinned and pinned by an older cluster", "10.100.0.50", []client.Object{olderPinned("older", "10.100.0.50")}, ReasonAddressInUse, ""},
		{"pinned and pinned by a younger cluster", "10.100.0.50", []client.Object{youngerPinned("younger", "10.100.0.50")}, "", "10.100.0.50"},
		{"free pick skips fabric ips and statuses", "", []client.Object{podFabricIP("ip1", subnetLabel, "10.100.0.10", fixtureNamespace, "x"), statusCluster("other", "10.100.0.11")}, "", "10.100.0.12"},
		{"free pick skips the pins of other clusters", "", []client.Object{youngerPinned("younger", "10.100.0.10")}, "", "10.100.0.11"},
		{"free pick skips the own pod only when it is the own pod", "", []client.Object{podFabricIP("ip1", subnetLabel, "10.100.0.10", fixtureNamespace, "kmc-prod-0")}, "", "10.100.0.10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.objects...)
			cluster := testCluster2("prod")
			cluster.CreationTimestamp = metav1.NewTime(epoch)
			cluster.Spec.ControlPlane.Address = tc.pinned
			resolved, gate := resolveCluster(t, h, cluster)
			if tc.reason != "" {
				requireGate(t, gate, tc.reason)
				return
			}
			if gate != nil || resolved.Address != tc.address {
				t.Fatalf("resolved %+v gate %+v, want %s", resolved, gate, tc.address)
			}
		})
	}
}

func statusCluster(name, address string) *v1alpha1.KubernetesCluster {
	cluster := testCluster2(name)
	cluster.Status.ControlPlane.Address = address
	return cluster
}

func olderPinned(name, address string) *v1alpha1.KubernetesCluster {
	cluster := testCluster2(name)
	cluster.CreationTimestamp = metav1.NewTime(epoch.Add(-time.Hour))
	cluster.Spec.ControlPlane.Address = address
	return cluster
}

func youngerPinned(name, address string) *v1alpha1.KubernetesCluster {
	cluster := testCluster2(name)
	cluster.CreationTimestamp = metav1.NewTime(epoch.Add(time.Hour))
	cluster.Spec.ControlPlane.Address = address
	return cluster
}

func TestResolveFullSubnetIsExhausted(t *testing.T) {
	objects := []client.Object{&v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "tiny"},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: fixtureNetwork, CIDR: "10.100.9.0/28"},
		Status:     v1alpha1.SubnetStatus{CommonStatus: v1alpha1.CommonStatus{Conditions: ready()}},
	}}
	h := newHarness(t, objects...)
	cluster := testCluster2("prod")
	cluster.Spec.Network.SubnetRef = "tiny"
	for _, last := range []string{"10", "11", "12", "13", "14"} {
		h.create(fabricIP("ip-"+last, network.FabricName(fixtureNamespace, "tiny"), "10.100.9."+last))
	}
	_, gate := resolveCluster(t, h, cluster)
	requireGate(t, gate, ReasonAddressExhausted)
}

func TestResolveExcludedRangesAreSkipped(t *testing.T) {
	var subnet v1alpha1.Subnet
	h := newHarness(t)
	_ = h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: "default"}, &subnet)
	subnet.Spec.ExcludeIPs = []string{"10.100.0.10..10.100.0.19"}
	if err := h.client.Update(context.Background(), &subnet); err != nil {
		t.Fatal(err)
	}
	resolved, gate := resolveCluster(t, h, testCluster2("prod"))
	if gate != nil || resolved.Address != "10.100.0.20" {
		t.Fatalf("resolved %+v gate %+v", resolved, gate)
	}
}

func TestGroupNamesAreSortedAndUnique(t *testing.T) {
	spec := testCluster().Spec
	spec.ControlPlane.SecurityGroups = []string{"z", "k8s"}
	spec.Workers = []v1alpha1.KubernetesWorker{testWorker("a"), testWorker("b")}
	got := GroupNames(spec)
	want := []string{"extra", "k8s", "z"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func pickAddress(t *testing.T, h *harness, cluster *v1alpha1.KubernetesCluster) string {
	t.Helper()
	resolved, gate := resolveCluster(t, h, cluster)
	if gate != nil {
		t.Fatalf("gate %+v", gate)
	}
	return resolved.Address
}

func TestTheFreePickAvoidsEveryPinnedAddress(t *testing.T) {
	vm := &v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "vm"},
		Spec:       v1alpha1.VirtualMachineSpec{InstanceType: "u1.nano", Networks: []v1alpha1.VMNetwork{{SubnetRef: "default", Address: "10.100.0.11"}}},
	}
	otherSubnetVM := &v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "vm2"},
		Spec:       v1alpha1.VirtualMachineSpec{InstanceType: "u1.nano", Networks: []v1alpha1.VMNetwork{{SubnetRef: "other", Address: "10.100.0.10"}}},
	}
	container := &v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "c"},
		Spec:       v1alpha1.ContainerSpec{Image: "x", Network: v1alpha1.ContainerNetwork{Address: "10.100.0.12"}},
	}
	otherWorkers := testCluster2("other")
	otherWorkers.Spec.Workers[0].Address = "10.100.0.13"
	own := testCluster2("prod")
	own.Spec.Workers[0].Address = "10.100.0.10"
	h := newHarness(t, vm, otherSubnetVM, container, otherWorkers)
	h.create(own)
	if got := pickAddress(t, h, own); got != "10.100.0.14" {
		t.Fatalf("picked %s: worker pins of this and other clusters, VM pins and container pins of the Subnet are taken", got)
	}
}

func TestAPinnedControlPlaneAddressRefusesAWorkerPin(t *testing.T) {
	own := testCluster2("prod")
	own.Spec.ControlPlane.Address = "10.100.0.50"
	own.Spec.Workers[0].Address = "10.100.0.50"
	h := newHarness(t)
	h.create(own)
	_, gate := resolveCluster(t, h, own)
	requireGate(t, gate, ReasonAddressInUse)
}
