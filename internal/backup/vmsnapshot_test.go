package backup

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/compute"
)

func decodeSnapshot(t *testing.T, obj *unstructured.Unstructured) map[string]any {
	t.Helper()
	raw := obj.GetAnnotations()[VMSnapshotAnnotation]
	if raw == "" {
		t.Fatalf("%s %s has no %s annotation", obj.GetKind(), obj.GetName(), VMSnapshotAnnotation)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func updateVM(t *testing.T, name string, change func(*v1alpha1.VirtualMachine)) *v1alpha1.VirtualMachine {
	t.Helper()
	vm := &v1alpha1.VirtualMachine{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: name}, vm); err != nil {
		t.Fatal(err)
	}
	change(vm)
	if err := k8sClient.Update(context.Background(), vm); err != nil {
		t.Fatal(err)
	}
	return vm
}

func setVMStatus(t *testing.T, name string, status v1alpha1.VirtualMachineStatus) {
	t.Helper()
	vm := &v1alpha1.VirtualMachine{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: name}, vm); err != nil {
		t.Fatal(err)
	}
	vm.Status = status
	if err := k8sClient.Status().Update(context.Background(), vm); err != nil {
		t.Fatal(err)
	}
}

func TestBackupStoresTheVMSpecOnTheVeleroBackup(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "snap-vm", "snap-data")
	updateVM(t, "snap-vm", func(vm *v1alpha1.VirtualMachine) {
		vm.Labels = map[string]string{"bedrock.cloudyfolks.io/note": "kept", "team": "dropped"}
		vm.Annotations = map[string]string{"bedrock.cloudyfolks.io/owner": "kept", HoldAnnotation: "another-restore", "note": "dropped"}
	})
	setVMStatus(t, "snap-vm", v1alpha1.VirtualMachineStatus{Phase: v1alpha1.VMPhaseRunning, Node: "node-a"})
	createVMB(t, "snap", v1alpha1.VirtualMachineBackupSpec{VMRef: "snap-vm"})
	reconcileVMB(t, newBackupReconciler(testNow), "snap")
	backup, err := getVelero(t, "Backup", VeleroName("vmb", projectNamespace, "snap"))
	if err != nil {
		t.Fatal(err)
	}
	vm := &v1alpha1.VirtualMachine{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "snap-vm"}, vm); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "bedrock.cloudyfolks.io/v1alpha1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      "snap-vm",
			"namespace": projectNamespace,
			"labels":    map[string]any{"bedrock.cloudyfolks.io/note": "kept", BackupVMLabel: "snap-vm", ExcludeFromBackup: "true"},
			"annotations": map[string]any{
				"bedrock.cloudyfolks.io/owner": "kept",
			},
		},
		"spec": asMap(t, vm.Spec),
	}
	if got := decodeSnapshot(t, backup); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot\n got %v\nwant %v", got, want)
	}
}

func TestBackupScheduleCarriesTheCurrentVMSpec(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "schedsnap-vm")
	createVMB(t, "schedsnap", v1alpha1.VirtualMachineBackupSpec{VMRef: "schedsnap-vm", Schedule: "0 2 * * *"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "schedsnap")
	instanceType := func() string {
		schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, "schedsnap"))
		if err != nil {
			t.Fatal(err)
		}
		spec, _ := decodeSnapshot(t, schedule)["spec"].(map[string]any)
		value, _ := spec["instanceType"].(string)
		return value
	}
	if got := instanceType(); got != "small" {
		t.Fatalf("instance type %q", got)
	}
	updateVM(t, "schedsnap-vm", func(vm *v1alpha1.VirtualMachine) { vm.Spec.InstanceType = "large" })
	reconcileVMB(t, r, "schedsnap")
	if got := instanceType(); got != "large" {
		t.Fatalf("instance type after the change %q", got)
	}
}

func TestBackupSelectorsCoverTheCloudInitSecret(t *testing.T) {
	scope := fixtureScope
	namespace := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: scope.Namespace, Labels: projectLabels}}
	labels, ok := compute.OwnerLabels("VirtualMachine", "web", namespace)
	if !ok {
		t.Fatal("the namespace is not a project namespace")
	}
	vm := v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: scope.Namespace}}
	secret := compute.CloudInitSecret(vm, labels, "#cloud-config\n", "")
	selectors, _, _ := unstructured.NestedSlice(Backup(scope, "daily", "web", OneShotTTL).Object, "spec", "orLabelSelectors")
	for _, selector := range selectors {
		matchLabels, _, _ := unstructured.NestedStringMap(selector.(map[string]any), "matchLabels")
		if labelsInclude(secret.Labels, matchLabels) {
			return
		}
	}
	t.Fatalf("no selector of %v matches the labels %v of the cloud-init secret", selectors, secret.Labels)
}

func labelsInclude(have, want map[string]string) bool {
	for key, value := range want {
		if have[key] != value {
			return false
		}
	}
	return true
}

func TestSnapshotOfKeepsTheSpecAndTheOwnedMetadata(t *testing.T) {
	vm := v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: "acme-prod", UID: "uid-1", ResourceVersion: "42",
			Labels:          map[string]string{"bedrock.cloudyfolks.io/note": "kept", "team": "dropped"},
			Annotations:     map[string]string{"bedrock.cloudyfolks.io/owner": "kept", HoldAnnotation: "restore-1", "kubectl.kubernetes.io/last-applied-configuration": "{}"},
			OwnerReferences: []metav1.OwnerReference{{Name: "someone"}},
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		},
		Spec:   v1alpha1.VirtualMachineSpec{InstanceType: "small", RootDisk: v1alpha1.VMRootDisk{SizeGi: 10}, Running: true},
		Status: v1alpha1.VirtualMachineStatus{Phase: v1alpha1.VMPhaseRunning},
	}
	snapshot, err := SnapshotOf(vm)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(snapshot), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "bedrock.cloudyfolks.io/v1alpha1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name": "web", "namespace": "acme-prod",
			"labels":      map[string]any{"bedrock.cloudyfolks.io/note": "kept"},
			"annotations": map[string]any{"bedrock.cloudyfolks.io/owner": "kept"},
		},
		"spec": asMap(t, vm.Spec),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot\n got %v\nwant %v", got, want)
	}
}

func TestVMFromSnapshotRestoresTheSpecAndTheOwnedMetadata(t *testing.T) {
	vm := v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "acme-prod", Labels: map[string]string{ExcludeFromBackup: "true"}, Annotations: map[string]string{"bedrock.cloudyfolks.io/owner": "kept"}},
		Spec:       v1alpha1.VirtualMachineSpec{InstanceType: "small", RootDisk: v1alpha1.VMRootDisk{SizeGi: 10}, Disks: []v1alpha1.VMDisk{{VolumeRef: "data"}}, Running: true},
	}
	snapshot, err := SnapshotOf(vm)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := VMFromSnapshot(snapshot, "web")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Name != "web" || restored.Namespace != "" || !reflect.DeepEqual(restored.Spec, vm.Spec) || !reflect.DeepEqual(restored.Labels, vm.Labels) || !reflect.DeepEqual(restored.Annotations, vm.Annotations) {
		t.Fatalf("restored %+v", restored)
	}
}

func TestVMFromSnapshotRefusesJunk(t *testing.T) {
	cases := map[string]string{
		"not json":      "not json",
		"another name":  `{"apiVersion":"bedrock.cloudyfolks.io/v1alpha1","kind":"VirtualMachine","metadata":{"name":"other"},"spec":{"instanceType":"small","rootDisk":{"sizeGi":10}}}`,
		"another kind":  `{"apiVersion":"bedrock.cloudyfolks.io/v1alpha1","kind":"Volume","metadata":{"name":"web"},"spec":{}}`,
		"another group": `{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"web"},"spec":{}}`,
	}
	for name, snapshot := range cases {
		if _, err := VMFromSnapshot(snapshot, "web"); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestWithSnapshotLeavesTheInputAlone(t *testing.T) {
	input := Backup(fixtureScope, "daily", "web", OneShotTTL)
	output := WithSnapshot(input, "{}")
	if _, found := input.GetAnnotations()[VMSnapshotAnnotation]; found {
		t.Fatal("the input changed")
	}
	if got := output.GetAnnotations()[VMSnapshotAnnotation]; got != "{}" || !strings.HasPrefix(output.GetName(), "vmb-") {
		t.Fatalf("output %v", output.Object)
	}
}
