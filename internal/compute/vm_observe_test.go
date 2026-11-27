package compute

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func setKubeVirtStatus(t *testing.T, c client.Client, ns, name string, status map[string]any) {
	t.Helper()
	obj := requirePresent(t, c, kubeVirtVMGVK, ns, name)
	obj.Object["status"] = status
	must(t, c.Status().Update(context.Background(), obj))
}

func createVMI(t *testing.T, c client.Client, ns, name, node string, conditions ...string) {
	t.Helper()
	items := make([]any, 0, len(conditions))
	for _, cond := range conditions {
		items = append(items, map[string]any{"type": cond, "status": "True"})
	}
	vmi := emptyOf(kubeVirtVMIGVK)
	vmi.SetNamespace(ns)
	vmi.SetName(name)
	vmi.Object["spec"] = map[string]any{"domain": map[string]any{"devices": map[string]any{}}}
	vmi.Object["status"] = map[string]any{"nodeName": node, "conditions": items}
	must(t, c.Create(context.Background(), vmi))
}

func createLauncher(t *testing.T, c client.Client, ns, name, vm, node string, annotations map[string]string) {
	t.Helper()
	createObject(t, c, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: annotations, Labels: map[string]string{launcherPodLabel: vm, "kubevirt.io": "virt-launcher"}},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c", Image: "registry.test/c"}}},
	})
}

func createElasticIP(t *testing.T, c client.Client, ns, name, target, address string) *v1alpha1.ElasticIP {
	t.Helper()
	ip := &v1alpha1.ElasticIP{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       v1alpha1.ElasticIPSpec{ProviderNetwork: "public"},
	}
	if target != "" {
		ip.Spec.Target = &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetVirtualMachine, Name: target}
	}
	createObject(t, c, ip)
	ip.Status.Address = address
	must(t, c.Status().Update(context.Background(), ip))
	return ip
}

func runningVM(t *testing.T, c client.Client, ns string) *VirtualMachineReconciler {
	t.Helper()
	createVM(t, c, ns, "web-1", noMutation)
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	setKubeVirtStatus(t, c, ns, "web-1", map[string]any{"printableStatus": "Running"})
	return r
}

func TestVMStatusFromKubeVirt(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	createVMI(t, c, ns, "web-1", "node-1", "AgentConnected", "LiveMigratable")
	createLauncher(t, c, ns, "launcher-1", "web-1", "node-1", addressAnnotationsIn(ns, "default", "10.100.0.7", "52:54:00:aa:bb:cc"))
	createElasticIP(t, c, ns, "ip1", "web-1", "198.51.100.10")
	createElasticIP(t, c, ns, "other", "web-2", "198.51.100.11")
	reconcileVM(t, r, ns, "web-1")
	vm := getVM(t, c, ns, "web-1")
	status := vm.Status
	if status.Phase != "Running" || status.Node != "node-1" || status.ElasticIP != "198.51.100.10" || status.ObservedGeneration != vm.Generation {
		t.Fatalf("status %+v", status)
	}
	if !reflect.DeepEqual(status.Addresses, []v1alpha1.VMAddress{{Subnet: "default", IP: "10.100.0.7", MAC: "52:54:00:aa:bb:cc"}}) {
		t.Fatalf("addresses %+v", status.Addresses)
	}
	if status.Console == nil || *status.Console != ConsolePaths(ns, "web-1") {
		t.Fatalf("console %+v", status.Console)
	}
	if status.GuestAgent == nil || *status.GuestAgent != (v1alpha1.VMGuestAgent{Version: "5.0.0", OS: "Ubuntu 24.04 LTS", Hostname: "web-1"}) {
		t.Fatalf("guest agent %+v", status.GuestAgent)
	}
	requireReason(t, vm, "Running")
	for _, name := range []string{ConditionGuestAgent, ConditionLiveMigratable} {
		if cond := conditionOf(vm, name); cond == nil || cond.Status != metav1.ConditionTrue {
			t.Errorf("%s %+v", name, cond)
		}
	}
	if cond := readyOf(vm); cond.ObservedGeneration != vm.Generation {
		t.Errorf("condition generation %d", cond.ObservedGeneration)
	}
}

func addressAnnotationsIn(ns, subnet, ip, mac string) map[string]string {
	prefix := network.SubnetProvider(ns, subnet) + ".cloudyfolks.io/"
	return map[string]string{prefix + "ip_address": ip, prefix + "mac_address": mac, prefix + "allocated": "true"}
}

func TestVMStatusDuringMigration(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	setKubeVirtStatus(t, c, ns, "web-1", map[string]any{"printableStatus": "Migrating"})
	createVMI(t, c, ns, "web-1", "node-2")
	createLauncher(t, c, ns, "source", "web-1", "node-1", addressAnnotationsIn(ns, "default", "10.100.0.7", "52:54:00:aa:bb:cc"))
	createLauncher(t, c, ns, "target", "web-1", "node-2", addressAnnotationsIn(ns, "default", "10.100.0.9", "52:54:00:aa:bb:cc"))
	reconcileVM(t, r, ns, "web-1")
	status := getVM(t, c, ns, "web-1").Status
	if status.Phase != "Migrating" || len(status.Addresses) != 1 || status.Addresses[0].IP != "10.100.0.9" {
		t.Fatalf("status %+v", status)
	}
}

func TestVMStatusKeepsGuestAgentOnFetchError(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	createVMI(t, c, ns, "web-1", "node-1", "AgentConnected")
	reconcileVM(t, r, ns, "web-1")
	r.GuestInfo = func(context.Context, string, string) (v1alpha1.VMGuestAgent, error) {
		return v1alpha1.VMGuestAgent{}, apierrors.NewServiceUnavailable("down")
	}
	reconcileVM(t, r, ns, "web-1")
	if agent := getVM(t, c, ns, "web-1").Status.GuestAgent; agent == nil || agent.Version != "5.0.0" {
		t.Fatalf("guest agent %+v", agent)
	}
}

func TestVMElasticIPFollowsTheTarget(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	ip := createElasticIP(t, c, ns, "ip1", "web-1", "198.51.100.10")
	reconcileVM(t, r, ns, "web-1")
	if got := getVM(t, c, ns, "web-1").Status.ElasticIP; got != "198.51.100.10" {
		t.Fatalf("elastic IP %q", got)
	}
	ip.Spec.Target = nil
	must(t, c.Update(context.Background(), ip))
	reconcileVM(t, r, ns, "web-1")
	if got := getVM(t, c, ns, "web-1").Status.ElasticIP; got != "" {
		t.Fatalf("elastic IP %q", got)
	}
}

func TestElasticIPTargetsMapToTheVM(t *testing.T) {
	targeted := &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-web", Name: "ip"}, Spec: v1alpha1.ElasticIPSpec{Target: &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetVirtualMachine, Name: "web-1"}}}
	got := targetedVM(context.Background(), targeted)
	if len(got) != 1 || got[0].Namespace != "acme-web" || got[0].Name != "web-1" {
		t.Fatalf("%+v", got)
	}
	balancer := &v1alpha1.ElasticIP{Spec: v1alpha1.ElasticIPSpec{Target: &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetLoadBalancer, Name: "lb"}}}
	if targetedVM(context.Background(), balancer) != nil || targetedVM(context.Background(), &v1alpha1.ElasticIP{}) != nil {
		t.Fatal("only virtual machine targets map")
	}
}

func TestElasticIPForPicksTheFirstByName(t *testing.T) {
	target := &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetVirtualMachine, Name: "web-1"}
	ips := []v1alpha1.ElasticIP{
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: v1alpha1.ElasticIPSpec{Target: target}, Status: v1alpha1.ElasticIPStatus{Address: "2.2.2.2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Spec: v1alpha1.ElasticIPSpec{Target: target}, Status: v1alpha1.ElasticIPStatus{Address: "1.1.1.1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "c"}, Status: v1alpha1.ElasticIPStatus{Address: "3.3.3.3"}},
	}
	if got := elasticIPFor(ips, "web-1"); got != "1.1.1.1" {
		t.Fatal(got)
	}
	if got := elasticIPFor(ips, "web-2"); got != "" {
		t.Fatal(got)
	}
}

func TestVMStatusWhileGated(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	createVMI(t, c, ns, "web-1", "node-1")
	createLauncher(t, c, ns, "launcher-1", "web-1", "node-1", addressAnnotationsIn(ns, "default", "10.100.0.7", "52:54:00:aa:bb:cc"))
	reconcileVM(t, r, ns, "web-1")
	before := requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").GetResourceVersion()
	must(t, c.Delete(context.Background(), &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web"}}))
	reconcileVM(t, r, ns, "web-1")
	vm := getVM(t, c, ns, "web-1")
	requireReason(t, vm, "SecurityGroupNotReady")
	if network := conditionOf(vm, ConditionNetworkReady); network == nil || network.Status != metav1.ConditionFalse {
		t.Fatalf("network %+v", network)
	}
	if vm.Status.Phase != "Running" || vm.Status.Node != "node-1" || len(vm.Status.Addresses) != 1 {
		t.Fatalf("status %+v", vm.Status)
	}
	if after := requirePresent(t, c, kubeVirtVMGVK, ns, "web-1").GetResourceVersion(); after != before {
		t.Fatalf("the KubeVirt VM changed: %s -> %s", before, after)
	}
}

func TestVMStatusIsNotRewrittenWhenUnchanged(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	reconcileVM(t, r, ns, "web-1")
	first := getVM(t, c, ns, "web-1").ResourceVersion
	reconcileVM(t, r, ns, "web-1")
	if second := getVM(t, c, ns, "web-1").ResourceVersion; second != first {
		t.Fatalf("resource version %s -> %s", first, second)
	}
}

func TestVMRestartRequiredCondition(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	setKubeVirtStatus(t, c, ns, "web-1", map[string]any{
		"printableStatus": "Running",
		"conditions":      []any{map[string]any{"type": "RestartRequired", "status": "True"}},
	})
	reconcileVM(t, r, ns, "web-1")
	if cond := conditionOf(getVM(t, c, ns, "web-1"), ConditionRestartRequired); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("%+v", cond)
	}
}

func TestVMStatusBeforeKubeVirtExists(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	ensureClusterImage(t, c, "ubuntu-24.04", v1alpha1.ImageTypeDisk, "ubuntu-24-04", v1alpha1.ImagePhaseImporting, 0, "ubuntu")
	createVM(t, c, ns, "web-1", noMutation)
	createVM(t, c, ns, "idle", func(vm *v1alpha1.VirtualMachine) {
		vm.Spec.Running = false
		vm.Spec.Disks = nil
	})
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	reconcileVM(t, r, ns, "idle")
	for name, phase := range map[string]string{"web-1": "Starting", "idle": "Stopped"} {
		vm := getVM(t, c, ns, name)
		requireReason(t, vm, "ImageNotReady")
		if vm.Status.Phase != phase || vm.Status.Node != "" || len(vm.Status.Addresses) != 0 || vm.Status.Console != nil {
			t.Errorf("%s status %+v", name, vm.Status)
		}
	}
	ensureClusterImage(t, c, "ubuntu-24.04", v1alpha1.ImageTypeDisk, "ubuntu-24-04", v1alpha1.ImagePhaseReady, 3<<30, "ubuntu")
}

func TestVMStatusStopped(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.Running = false })
	r := newVMReconciler(c)
	reconcileVM(t, r, ns, "web-1")
	setKubeVirtStatus(t, c, ns, "web-1", map[string]any{"printableStatus": "Stopped"})
	result := reconcileVM(t, r, ns, "web-1")
	vm := getVM(t, c, ns, "web-1")
	requireReason(t, vm, "Stopped")
	if vm.Status.Phase != "Stopped" || vm.Status.Console != nil || readyOf(vm).Status != metav1.ConditionTrue || result.RequeueAfter != 0 {
		t.Fatalf("status %+v requeue %s", vm.Status, result.RequeueAfter)
	}
}

func TestRunningVMsRequeue(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	if got := reconcileVM(t, r, ns, "web-1").RequeueAfter; got != 5*time.Minute {
		t.Fatalf("running requeue %s", got)
	}
}

func snapshotObject(ns, name, vm string, annotations map[string]string) *unstructured.Unstructured {
	obj := emptyOf(vmSnapshotGVK)
	obj.SetNamespace(ns)
	obj.SetName(name)
	obj.SetLabels(map[string]string{v1alpha1.LabelKind: "VirtualMachine", v1alpha1.LabelName: vm})
	obj.SetAnnotations(annotations)
	obj.Object["spec"] = map[string]any{"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vm}}
	return obj
}

func TestExpiredSnapshots(t *testing.T) {
	now := fixedNow
	at := func(name string, annotations map[string]string) unstructured.Unstructured {
		return *snapshotObject("ns", name, "web-1", annotations)
	}
	snapshots := []unstructured.Unstructured{
		at("past", map[string]string{AnnotationExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)}),
		at("future", map[string]string{AnnotationExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}),
		at("plain", nil),
		at("garbage", map[string]string{AnnotationExpiresAt: "tomorrow"}),
	}
	got := expiredSnapshots(snapshots, now)
	if len(got) != 1 || got[0].GetName() != "past" {
		t.Fatalf("%+v", got)
	}
}

func TestVMPrunesExpiredSnapshots(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	r := runningVM(t, c, ns)
	createObject(t, c, snapshotObject(ns, "past", "web-1", map[string]string{AnnotationExpiresAt: fixedNow.Add(-time.Hour).Format(time.RFC3339)}))
	createObject(t, c, snapshotObject(ns, "future", "web-1", map[string]string{AnnotationExpiresAt: fixedNow.Add(time.Hour).Format(time.RFC3339)}))
	createObject(t, c, snapshotObject(ns, "plain", "web-1", nil))
	reconcileVM(t, r, ns, "web-1")
	snapshot := func(name string) error {
		_, err := getUnstructured(c, vmSnapshotGVK, ns, name)
		return err
	}
	if err := snapshot("past"); !apierrors.IsNotFound(err) {
		t.Errorf("the expired snapshot stays: %v", err)
	}
	for _, name := range []string{"future", "plain"} {
		if err := snapshot(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRefs(t *testing.T) {
	vm := fixtureVM("acme-web", "web-1")
	vm.Spec.Networks[0].ElasticIPRef = "ip"
	want := []string{"image/cluster/ubuntu-24.04", "securitygroup/web", "sshkey/alice", "subnet/default", "volume/data1"}
	if got := Refs(vm); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	vm.Spec.CDROMs = []v1alpha1.VMCDROM{{ImageRef: "install-iso"}}
	vm.Spec.Networks = nil
	vm.Spec.Image = &v1alpha1.ImageRef{Name: "mine", Scope: v1alpha1.ImageScopeProject}
	want = []string{"image/cluster/install-iso", "image/project/install-iso", "image/project/mine", "sshkey/alice", "subnet/default", "volume/data1"}
	if got := Refs(vm); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestVMStatusReportsTheMigrationOfItsOwnVM(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	other := uniqueNamespace(t, c)
	r := runningVM(t, c, ns)
	createVMI(t, c, ns, "web-1", "node-1")
	createMigration := func(namespace, name, vmi, phase string) {
		migration := emptyOf(kubeVirtMigrationGVK)
		migration.SetNamespace(namespace)
		migration.SetName(name)
		migration.Object["spec"] = map[string]any{"vmiName": vmi}
		must(t, c.Create(context.Background(), migration))
		migration.Object["status"] = map[string]any{"phase": phase}
		must(t, c.Status().Update(context.Background(), migration))
	}
	createMigration(ns, "web-1-migrate-a", "web-1", "Succeeded")
	reconcileVM(t, r, ns, "web-1")
	status := getVM(t, c, ns, "web-1").Status
	if status.Migration == nil || status.Migration.Name != "web-1-migrate-a" || status.Migration.Phase != "Succeeded" || status.Migration.StartedAt == nil {
		t.Fatalf("migration %+v", status.Migration)
	}
	createMigration(other, "foreign", "web-1", "Running")
	createMigration(ns, "web-2-migrate", "web-2", "Running")
	reconcileVM(t, r, ns, "web-1")
	if got := getVM(t, c, ns, "web-1").Status.Migration; got == nil || got.Name != "web-1-migrate-a" {
		t.Fatalf("migration of another VM or namespace leaked: %+v", got)
	}
}
