package operator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const gib = int64(1) << 30

func preflightTime() time.Time {
	return time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
}

func healthyHost(name string) v1alpha1.Host {
	notAfter := metav1.NewTime(preflightTime().Add(30 * 24 * time.Hour))
	host := v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	host.Status = v1alpha1.HostStatus{
		Hostname: name,
		Checks:   &v1alpha1.HostChecks{TimeSynced: true, VarLibFreeBytes: 20 * gib, CertificatesNotAfter: &notAfter},
	}
	return host
}

func readyNode(name, arch string) corev1.Node {
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	node.Status.NodeInfo.Architecture = arch
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	return node
}

func healthyEtcd(hosts []v1alpha1.Host) []v1alpha1.Host {
	members := int32(len(hostsWithRole(hosts, v1alpha1.RoleControlPlane)))
	settled := make([]v1alpha1.Host, 0, len(hosts))
	for _, host := range hosts {
		copied := *host.DeepCopy()
		if v1alpha1.HostHasRole(copied, v1alpha1.RoleControlPlane) && copied.Status.Checks != nil {
			copied.Status.Checks.EtcdMembers, copied.Status.Checks.EtcdHealthy = members, true
		}
		settled = append(settled, copied)
	}
	return settled
}

func healthyPreflight() preflightInput {
	depotHost := healthyHost("node-a")
	depotHost.Status.Depot = &v1alpha1.DepotStatus{URL: "http://10.0.0.11:9480", Bundles: []v1alpha1.DepotBundle{{Version: "v2", Arch: "amd64", Bytes: 3 * gib}, {Version: "v2", Arch: "arm64", Bytes: 4 * gib}}}
	target := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v2"}, Spec: v1alpha1.ReleaseSpec{Version: "v2", UpgradeFrom: []string{"v1"}}}
	return preflightInput{
		Running: "v1",
		To:      "v2",
		Target:  target,
		Hosts:   healthyEtcd([]v1alpha1.Host{depotHost, healthyHost("node-b")}),
		Nodes:   []corev1.Node{readyNode("node-a", "amd64"), readyNode("node-b", "arm64")},
		Ceph:    cephReport{Health: "HEALTH_OK", PGs: 8, CleanPGs: 8},
		Now:     preflightTime(),
	}
}

func vmi(name string, migratable bool) unstructured.Unstructured {
	status := "False"
	if migratable {
		status = "True"
	}
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachineInstance",
		"metadata": map[string]any{"name": name, "namespace": "tenant-a"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "LiveMigratable", "status": status, "reason": "DisksNotLiveMigratable"}}},
	}}
}

func vm(name string, annotations map[string]any) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine",
		"metadata": map[string]any{"name": name, "namespace": "tenant-a", "annotations": annotations},
	}}
}

func TestPreflightProblems(t *testing.T) {
	cases := map[string]struct {
		change func(preflightInput) preflightInput
		want   string
	}{
		"healthy": {func(in preflightInput) preflightInput { return in }, ""},
		"release missing": {func(in preflightInput) preflightInput {
			in.Target = nil
			return in
		}, "release: Release/v2 does not exist"},
		"release mismatch": {func(in preflightInput) preflightInput {
			target := in.Target.DeepCopy()
			target.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "SpecMismatch", Message: "release object differs from the embedded release"}}
			in.Target = target
			return in
		}, "release: Release/v2 is not ready: release object differs from the embedded release"},
		"no upgrade path": {func(in preflightInput) preflightInput {
			target := in.Target.DeepCopy()
			target.Spec.UpgradeFrom = []string{"v0"}
			in.Target = target
			return in
		}, "release: v2 does not list v1 in upgradeFrom"},
		"no depot for an arch": {func(in preflightInput) preflightInput {
			in.Nodes = append(in.Nodes, readyNode("node-c", "riscv64"))
			in.Hosts = healthyEtcd(append(in.Hosts, healthyHost("node-c")))
			return in
		}, "depot: no host serves v2 for riscv64"},
		"node not ready": {func(in preflightInput) preflightInput {
			in.Nodes[1].Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}
			return in
		}, "nodes: node-b is not Ready"},
		"hostname differs": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Hostname = "worker-7"
			return in
		}, "nodes: host node-b reports hostname \"worker-7\", its Node is node-b"},
		"host without a node": {func(in preflightInput) preflightInput {
			in.Hosts = healthyEtcd(append(in.Hosts, healthyHost("controller-only")))
			return in
		}, ""},
		"host without checks": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks = nil
			return in
		}, "hosts: node-b reports no checks; etcd: node-b reports 0 of 2 members"},
		"etcd member missing": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.EtcdMembers = 1
			return in
		}, "etcd: node-b reports 1 of 2 members"},
		"etcd member unhealthy": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.EtcdHealthy = false
			return in
		}, "etcd: node-b is not healthy"},
		"one controller needs only its own etcd": {func(in preflightInput) preflightInput {
			in.Hosts[1].Spec.Roles = []string{v1alpha1.RoleWorkload}
			in.Hosts[1].Status.Checks.EtcdMembers, in.Hosts[1].Status.Checks.EtcdHealthy = 0, false
			in.Hosts = healthyEtcd(in.Hosts)
			return in
		}, ""},
		"one controller with unhealthy etcd": {func(in preflightInput) preflightInput {
			in.Hosts[1].Spec.Roles = []string{v1alpha1.RoleWorkload}
			in.Hosts = healthyEtcd(in.Hosts)
			in.Hosts[0].Status.Checks.EtcdHealthy = false
			return in
		}, "etcd: node-a is not healthy"},
		"certificate expires soon": {func(in preflightInput) preflightInput {
			soon := metav1.NewTime(preflightTime().Add(6 * 24 * time.Hour))
			in.Hosts[0].Status.Checks.CertificatesNotAfter = &soon
			return in
		}, "certificates: node-a expires 2026-10-07T10:00:00Z, less than 7 days away"},
		"certificate unknown": {func(in preflightInput) preflightInput {
			in.Hosts[0].Status.Checks.CertificatesNotAfter = nil
			return in
		}, "certificates: node-a reports no expiry"},
		"clock not synced": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.TimeSynced = false
			return in
		}, "time: node-b clock is not synchronized"},
		"disk too small": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.VarLibFreeBytes = 7 * gib
			return in
		}, "disk: node-b has 7.0 GiB free in /var/lib, needs 8.0 GiB"},
		"disk too small on the backup host counts its images twice": {func(in preflightInput) preflightInput {
			in.Hosts[0].Status.Checks.ImagesBytes = 4 * gib
			in.Hosts[0].Status.Checks.VarLibFreeBytes = 13 * gib
			return in
		}, "disk: node-a has 13.0 GiB free in /var/lib, needs 14.0 GiB"},
		"images on a non backup host do not count": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.ImagesBytes = 100 * gib
			return in
		}, ""},
		"the backup host needs twice its images and the kubelet reserve": {func(in preflightInput) preflightInput {
			in.Hosts[0].Status.Checks.ImagesBytes = 4 * gib
			in.Hosts[0].Status.Checks.VarLibSizeBytes = 40 * gib
			in.Hosts[0].Status.Checks.VarLibFreeBytes = 19 * gib
			return in
		}, "disk: node-a has 19.0 GiB free in /var/lib, needs 20.0 GiB"},
		"the backup host with room for its images and the kubelet reserve": {func(in preflightInput) preflightInput {
			in.Hosts[0].Status.Checks.ImagesBytes = 4 * gib
			in.Hosts[0].Status.Checks.VarLibSizeBytes = 40 * gib
			in.Hosts[0].Status.Checks.VarLibFreeBytes = 20 * gib
			return in
		}, ""},
		"another host needs its bundle and the kubelet reserve": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.ImagesBytes = 100 * gib
			in.Hosts[1].Status.Checks.VarLibSizeBytes = 40 * gib
			in.Hosts[1].Status.Checks.VarLibFreeBytes = 13 * gib
			return in
		}, "disk: node-b has 13.0 GiB free in /var/lib, needs 14.0 GiB"},
		"a host without a node needs no kubelet reserve": {func(in preflightInput) preflightInput {
			controller := healthyHost("controller-only")
			controller.Status.Checks.VarLibSizeBytes = 40 * gib
			controller.Status.Checks.VarLibFreeBytes = 8 * gib
			in.Hosts = healthyEtcd(append(in.Hosts, controller))
			return in
		}, ""},
		"an unknown filesystem size adds no reserve": {func(in preflightInput) preflightInput {
			in.Hosts[1].Status.Checks.VarLibFreeBytes = 8 * gib
			return in
		}, ""},
		"ceph warn": {func(in preflightInput) preflightInput {
			in.Ceph = cephReport{Health: "HEALTH_WARN", PGs: 8, CleanPGs: 8}
			return in
		}, "ceph: health is HEALTH_WARN"},
		"ceph unreadable": {func(in preflightInput) preflightInput {
			in.CephError = "no running rook-ceph-operator pod in rook-ceph"
			return in
		}, "ceph: no running rook-ceph-operator pod in rook-ceph"},
		"vm that can migrate": {func(in preflightInput) preflightInput {
			in.VMIs = []unstructured.Unstructured{vmi("web", true)}
			return in
		}, ""},
		"vm that may shut down": {func(in preflightInput) preflightInput {
			in.VMIs = []unstructured.Unstructured{vmi("db", false)}
			in.VMs = []unstructured.Unstructured{vm("db", map[string]any{allowShutdownAnnotation: "true"})}
			return in
		}, ""},
		"vm that must not stop": {func(in preflightInput) preflightInput {
			in.VMIs = []unstructured.Unstructured{vmi("db", false)}
			in.VMs = []unstructured.Unstructured{vm("db", map[string]any{allowShutdownAnnotation: "false"})}
			return in
		}, "vms: tenant-a/db cannot live-migrate (DisksNotLiveMigratable) and lacks bedrock.cloudyfolks.io/allow-shutdown=true"},
		"two problems": {func(in preflightInput) preflightInput {
			in.Hosts[0].Status.Checks.TimeSynced = false
			in.Hosts[1].Status.Checks.TimeSynced = false
			return in
		}, "time: node-a clock is not synchronized; time: node-b clock is not synchronized"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := preflightProblem(tc.change(healthyPreflight())); got != tc.want {
				t.Fatalf("problem %q, want %q", got, tc.want)
			}
		})
	}
}

func createHostWithStatus(t *testing.T, ctx context.Context, c client.Client, host v1alpha1.Host) {
	t.Helper()
	status := host.Status
	if err := c.Create(ctx, &host); err != nil {
		t.Fatal(err)
	}
	host.Status = status
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
}

func createNodeWithStatus(t *testing.T, ctx context.Context, c client.Client, node corev1.Node) {
	t.Helper()
	status := node.Status
	if err := c.Create(ctx, &node); err != nil {
		t.Fatal(err)
	}
	node.Status = status
	if err := c.Status().Update(ctx, &node); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightPhase(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	notAfter := metav1.NewTime(time.Now().Add(90 * 24 * time.Hour))
	host := v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	host.Status = v1alpha1.HostStatus{
		Hostname: "node-a",
		Checks:   &v1alpha1.HostChecks{TimeSynced: true, VarLibFreeBytes: 20 * gib, CertificatesNotAfter: &notAfter, EtcdMembers: 1, EtcdHealthy: true},
		Depot:    &v1alpha1.DepotStatus{URL: "http://10.0.0.11:9480", Bundles: []v1alpha1.DepotBundle{{Version: "v2", Arch: "amd64", Bytes: 3 * gib}}},
	}
	createHostWithStatus(t, ctx, c, host)
	createNodeWithStatus(t, ctx, c, readyNode("node-a", "amd64"))
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhasePreflight))
	var calls []recordedExec
	env := upgradeEnv{Client: c, Exec: fakeExec(cephStatusJSON, nil, &calls)}

	result, err := preflight(ctx, env, getCluster(t, ctx, c))
	if err != nil || result.Blocked != "release: Release/v2 does not exist" {
		t.Fatalf("result %+v err %v", result, err)
	}
	target := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v2"}, Spec: v1alpha1.ReleaseSpec{Version: "v2", Image: "ghcr.io/cloudyfolks-labs/bedrock:v2", UpgradeFrom: []string{"v1"}}}
	if err := c.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	result, err = preflight(ctx, env, getCluster(t, ctx, c))
	if err != nil || !result.Done || len(calls) != 0 {
		t.Fatalf("result %+v err %v exec calls %d", result, err, len(calls))
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: storageNamespace}}); err != nil {
		t.Fatal(err)
	}
	createCephCluster(t, ctx, c)
	result, err = preflight(ctx, env, getCluster(t, ctx, c))
	if err != nil || result.Blocked != "ceph: no running rook-ceph-operator pod in rook-ceph" {
		t.Fatalf("result %+v err %v", result, err)
	}
	createRookOperatorPod(t, ctx, c, "rook-ceph-operator-0", corev1.PodRunning)
	result, err = preflight(ctx, env, getCluster(t, ctx, c))
	if err != nil || result.Blocked != "ceph: 3 of 33 PGs are not active+clean" {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestPreflightRunsInBothOperators(t *testing.T) {
	if oldPhases()[v1alpha1.PhasePreflight] == nil || newPhases()[v1alpha1.PhasePreflight] == nil {
		t.Fatal("both operators must run Preflight")
	}
}
