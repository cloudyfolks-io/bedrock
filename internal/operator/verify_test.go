package operator

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func TestSmokeVM(t *testing.T) {
	plain := smokeVM()
	volumes, _, _ := unstructured.NestedSlice(plain.Object, "spec", "template", "spec", "volumes")
	image, _, _ := unstructured.NestedString(volumes[0].(map[string]any), "containerDisk", "image")
	if plain.GetName() != smokeName || plain.GetNamespace() != release.SystemNamespace || image != "quay.io/kubevirt/cirros-container-disk-demo:v1.9.0" || len(volumes) != 1 {
		t.Fatalf("vm %+v", plain.Object)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(plain.Object, "spec", "template", "spec", "affinity"); found {
		t.Fatal("a plain smoke VM has no affinity")
	}

	full := awayFromOperator(withScratchDisk(plain, "block"))
	templates, _, _ := unstructured.NestedSlice(full.Object, "spec", "dataVolumeTemplates")
	class, _, _ := unstructured.NestedString(templates[0].(map[string]any), "spec", "storage", "storageClassName")
	size, _, _ := unstructured.NestedString(templates[0].(map[string]any), "spec", "storage", "resources", "requests", "storage")
	disks, _, _ := unstructured.NestedSlice(full.Object, "spec", "template", "spec", "domain", "devices", "disks")
	volumes, _, _ = unstructured.NestedSlice(full.Object, "spec", "template", "spec", "volumes")
	terms, _, _ := unstructured.NestedSlice(full.Object, "spec", "template", "spec", "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
	if class != "block" || size != "1Gi" || len(disks) != 2 || len(volumes) != 2 || len(terms) != 1 {
		t.Fatalf("vm %+v", full.Object["spec"])
	}
	topology, _, _ := unstructured.NestedString(terms[0].(map[string]any), "topologyKey")
	app, _, _ := unstructured.NestedString(terms[0].(map[string]any), "labelSelector", "matchLabels", "app")
	if topology != "kubernetes.io/hostname" || app != operatorDeployment {
		t.Fatalf("anti-affinity %+v", terms[0])
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(plain.Object, "spec", "dataVolumeTemplates"); found {
		t.Fatal("withScratchDisk must not change its input")
	}
}

func TestVMIAddress(t *testing.T) {
	instance := vmi("bedrock-smoke", true)
	if _, ok := vmiAddress(instance); ok {
		t.Fatal("a VMI without interfaces has no address")
	}
	instance.Object["status"] = map[string]any{"interfaces": []any{map[string]any{"name": "default"}, map[string]any{"name": "second", "ipAddress": "10.244.0.9"}}}
	if address, ok := vmiAddress(instance); !ok || address != "10.244.0.9:22" {
		t.Fatalf("address %q %v", address, ok)
	}
}

func TestDialTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	if err := dialTCP(context.Background(), address); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dialTCP(context.Background(), address); err == nil {
		t.Fatal("a closed port must fail")
	}
}

func setVMIStatus(t *testing.T, ctx context.Context, c client.Client, status map[string]any) {
	t.Helper()
	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(vmiGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: smokeName}, instance)
	if apierrors.IsNotFound(err) {
		instance = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachineInstance", "metadata": map[string]any{"name": smokeName, "namespace": release.SystemNamespace}}}
		if err := c.Create(ctx, instance); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	instance.Object["status"] = status
	if err := c.Status().Update(ctx, instance); err != nil {
		t.Fatal(err)
	}
}

func smokeVMExists(t *testing.T, ctx context.Context, c client.Client) (*unstructured.Unstructured, bool) {
	t.Helper()
	machine := &unstructured.Unstructured{}
	machine.SetGroupVersionKind(vmGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: smokeName}, machine)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return machine, true
}

func TestVerifyPhase(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	preloadedNodeUpgrade(t, ctx, c, "node-a")
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseVerify))
	dialErr := errors.New("connection refused")
	var dialed []string
	env := upgradeEnv{Client: c, Dial: func(_ context.Context, address string) error {
		dialed = append(dialed, address)
		return dialErr
	}}
	run := func(want phaseResult) {
		t.Helper()
		got, err := verify(ctx, env, getCluster(t, ctx, c))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("result %+v, want %+v", got, want)
		}
	}

	run(phaseResult{Message: "verify: starting the smoke VM"})
	machine, ok := smokeVMExists(t, ctx, c)
	if !ok {
		t.Fatal("the smoke VM must exist")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(machine.Object, "spec", "dataVolumeTemplates"); found {
		t.Fatal("without StorageClass block the smoke VM has no disk")
	}
	setVMIStatus(t, ctx, c, map[string]any{"phase": "Scheduling"})
	run(phaseResult{Message: "verify: smoke VM is Scheduling"})
	setVMIStatus(t, ctx, c, map[string]any{"phase": "Running", "interfaces": []any{map[string]any{"name": "default", "ipAddress": "10.244.0.9"}}})
	run(phaseResult{Message: "verify: smoke VM 10.244.0.9:22: connection refused"})
	dialErr = nil
	run(phaseResult{Message: "verify: smoke VM reached 10.244.0.9:22, disk skipped: no StorageClass block"})
	if _, ok := smokeVMExists(t, ctx, c); ok {
		t.Fatal("the smoke VM must be deleted")
	}
	if steps := getNodeUpgrade(t, ctx, c, "node-a").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepPrune}) {
		t.Fatalf("steps %v", steps)
	}
	run(phaseResult{Message: "verify: prune 0/1 nodes, waiting for node-a"})
	if len(dialed) != 2 {
		t.Fatalf("a passed smoke test must not run again: dialed %v", dialed)
	}
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPrune, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s ctr images rm: exit status 1"})
	run(phaseResult{Failure: "verify: prune failed on node-a: k0s ctr images rm: exit status 1"})
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPrune, State: v1alpha1.StepSucceeded, Attempt: 1})
	run(phaseResult{Done: true})
	if upgrades, err := listNodeUpgrades(ctx, c, "v2"); err != nil || len(upgrades) != 0 {
		t.Fatalf("the NodeUpgrade objects must be deleted: %d %v", len(upgrades), err)
	}
}

func TestVerifyPhaseSpreadsAndAttachesADisk(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	class := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "block"}, Provisioner: "rook-ceph.rbd.csi.ceph.com"}
	if err := c.Create(ctx, class); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseVerify))
	env := upgradeEnv{Client: c, Dial: func(context.Context, string) error { return nil }}
	if _, err := verify(ctx, env, getCluster(t, ctx, c)); err != nil {
		t.Fatal(err)
	}
	machine, ok := smokeVMExists(t, ctx, c)
	if !ok {
		t.Fatal("the smoke VM must exist")
	}
	want := awayFromOperator(withScratchDisk(smokeVM(), "block"))
	if !reflect.DeepEqual(machine.Object["spec"], want.Object["spec"]) {
		t.Fatalf("spec %+v", machine.Object["spec"])
	}
	setVMIStatus(t, ctx, c, map[string]any{"phase": "Running", "interfaces": []any{map[string]any{"ipAddress": "10.244.1.7"}}})
	got, err := verify(ctx, env, getCluster(t, ctx, c))
	if err != nil || got != (phaseResult{Message: "verify: smoke VM reached 10.244.1.7:22 with a 1Gi disk"}) {
		t.Fatalf("result %+v err %v", got, err)
	}
}

func TestVerifyPhaseTimesOut(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	status := upgradeStatusIn(v1alpha1.PhaseVerify)
	status.Upgrade.PhaseStartedAt = metav1.NewTime(time.Now().Add(-20 * time.Minute))
	createClusterWithStatus(t, ctx, c, "v2", status)
	env := upgradeEnv{Client: c, Dial: func(context.Context, string) error { return nil }}
	got, err := verify(ctx, env, getCluster(t, ctx, c))
	if err != nil || got != (phaseResult{Failure: "verify: smoke VM not reached within 15m0s: starting the smoke VM"}) {
		t.Fatalf("result %+v err %v", got, err)
	}
}

func TestVerifyPhaseTable(t *testing.T) {
	if newPhases()[v1alpha1.PhaseVerify] == nil || oldPhases()[v1alpha1.PhaseVerify] != nil {
		t.Fatal("only the new operator runs Verify")
	}
}
