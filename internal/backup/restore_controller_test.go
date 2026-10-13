package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type restoreFixture struct {
	vm, volume, vmb, backupID string
}

func fixtureOf(prefix string) restoreFixture {
	return restoreFixture{vm: prefix + "-web", volume: prefix + "-data", vmb: prefix + "-daily", backupID: "vmb-" + prefix + "-x-20261008020000"}
}

func createVMBWithBackups(t *testing.T, namespace string, fx restoreFixture) {
	t.Helper()
	ctx := context.Background()
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: fx.vmb, Namespace: namespace}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: fx.vm, Target: "default", Schedule: "0 2 * * *"}}
	if err := k8sClient.Create(ctx, vmb); err != nil {
		t.Fatal(err)
	}
	vmb.Status.Backups = []v1alpha1.VMBackupRecord{{ID: fx.backupID, Time: metav1.Now()}}
	if err := k8sClient.Status().Update(ctx, vmb); err != nil {
		t.Fatal(err)
	}
}

func createKubeVirtObjects(t *testing.T, fx restoreFixture) {
	t.Helper()
	ctx := context.Background()
	vmLabels := v1alpha1.TenantLabels("VirtualMachine", fx.vm, "acme", "prod")
	volumeLabels := v1alpha1.TenantLabels("Volume", fx.volume, "acme", "prod")
	kubevirt := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"runStrategy": "Halted", "template": map[string]any{"spec": map[string]any{"domain": map[string]any{"devices": map[string]any{}}}}}}}
	kubevirt.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"})
	kubevirt.SetNamespace(projectNamespace)
	kubevirt.SetName(fx.vm)
	kubevirt.SetLabels(vmLabels)
	if err := k8sClient.Create(ctx, kubevirt); err != nil {
		t.Fatal(err)
	}
	volume := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"source": map[string]any{"blank": map[string]any{}}, "storage": map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}}}}}
	volume.SetGroupVersionKind(schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"})
	volume.SetNamespace(projectNamespace)
	volume.SetName(fx.vm + "-root")
	volume.SetLabels(vmLabels)
	if err := k8sClient.Create(ctx, volume); err != nil {
		t.Fatal(err)
	}
	for name, labels := range map[string]map[string]string{fx.vm + "-root": vmLabels, fx.volume: volumeLabels} {
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: projectNamespace, Labels: labels}, Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		}}
		if err := k8sClient.Create(ctx, pvc); err != nil {
			t.Fatal(err)
		}
	}
}

func underlyingCount(t *testing.T, fx restoreFixture) int {
	t.Helper()
	ctx := context.Background()
	count := 0
	for _, gvk := range []schema.GroupVersionKind{kubevirtVMListGVK, dataVolumeListGVK, pvcListGVK} {
		for _, labels := range []map[string]string{v1alpha1.TenantLabels("VirtualMachine", fx.vm, "acme", "prod"), v1alpha1.TenantLabels("Volume", fx.volume, "acme", "prod")} {
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(gvk)
			if err := k8sClient.List(ctx, list, client.InNamespace(projectNamespace), client.MatchingLabels(labels)); err != nil {
				t.Fatal(err)
			}
			count += len(list.Items)
		}
	}
	return count
}

func settleUnderlying(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, gvk := range []schema.GroupVersionKind{kubevirtVMListGVK, dataVolumeListGVK, pvcListGVK} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := k8sClient.List(ctx, list, client.InNamespace(projectNamespace)); err != nil {
			t.Fatal(err)
		}
		for i := range list.Items {
			if list.Items[i].GetDeletionTimestamp() != nil {
				patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`))
				if err := k8sClient.Patch(ctx, &list.Items[i], patch); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func createVMR(t *testing.T, name, namespace string, spec v1alpha1.VirtualMachineRestoreSpec) {
	t.Helper()
	vmr := &v1alpha1.VirtualMachineRestore{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
	if err := k8sClient.Create(context.Background(), vmr); err != nil {
		t.Fatal(err)
	}
}

func newRestoreReconciler() *RestoreReconciler {
	return &RestoreReconciler{Client: k8sClient, Now: func() time.Time { return testNow }}
}

func reconcileVMR(t *testing.T, r *RestoreReconciler, namespace, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func getVMR(t *testing.T, name string) *v1alpha1.VirtualMachineRestore {
	t.Helper()
	vmr := &v1alpha1.VirtualMachineRestore{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: name}, vmr); err != nil {
		t.Fatal(err)
	}
	return vmr
}

func requireRestoreState(t *testing.T, vmr *v1alpha1.VirtualMachineRestore, phase string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var got *metav1.Condition
	for i := range vmr.Status.Conditions {
		if vmr.Status.Conditions[i].Type == v1alpha1.ConditionReady {
			got = &vmr.Status.Conditions[i]
		}
	}
	if got == nil || vmr.Status.Phase != phase || got.Status != status || got.Reason != reason {
		t.Fatalf("phase %q conditions %+v, want %s %s/%s", vmr.Status.Phase, vmr.Status.Conditions, phase, status, reason)
	}
}

func holdOf(t *testing.T, obj client.Object) string {
	t.Helper()
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	return obj.GetAnnotations()[HoldAnnotation]
}

func restoreName(vmr string) string { return VeleroName("vmr", projectNamespace, vmr) }

func setRestoreStatus(t *testing.T, vmr string, status map[string]any) {
	t.Helper()
	restore, err := getVelero(t, "Restore", restoreName(vmr))
	if err != nil {
		t.Fatal(err)
	}
	restore.Object["status"] = status
	if err := k8sClient.Update(context.Background(), restore); err != nil {
		t.Fatal(err)
	}
}

func readyForRestore(t *testing.T, fx restoreFixture) {
	t.Helper()
	setLocation(t, "Available")
	createFixtureBackup(t, fx.backupID, fx.vmb, "Completed", "2026-10-08T02:05:00Z")
}

func requireUnderlyingIntact(t *testing.T, fx restoreFixture) {
	t.Helper()
	if got := underlyingCount(t, fx); got != 4 {
		t.Fatalf("%d underlying objects left, want 4", got)
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(kubevirtVMListGVK)
	if err := k8sClient.List(context.Background(), list, client.InNamespace(projectNamespace), client.MatchingLabels(v1alpha1.TenantLabels("VirtualMachine", fx.vm, "acme", "prod"))); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.GetDeletionTimestamp() != nil {
			t.Fatalf("%s is being deleted", item.GetName())
		}
	}
}

func requireNoRestore(t *testing.T, vmr string) {
	t.Helper()
	_, err := getVelero(t, "Restore", restoreName(vmr))
	requireGone(t, err)
	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: VeleroNamespace, Name: restoreName(vmr) + "-modifiers"}, &corev1.ConfigMap{})
	requireGone(t, err)
}

func TestRestoreRefusesForeignBackupID(t *testing.T) {
	setLocation(t, "Available")
	fx := fixtureOf("foreign")
	createVM(t, fx.vm, fx.volume)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "foreign-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: "vmb-other-x-1", Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "foreign-restore")
	requireRestoreState(t, getVMR(t, "foreign-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, RestoreReasonBackupNotFound)
	requireNoRestore(t, "foreign-restore")
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "" {
		t.Fatalf("hold %q", hold)
	}
}

func TestRestoreReadsTheBackupRefFromItsOwnNamespace(t *testing.T) {
	fx := fixtureOf("crossns")
	if err := ensureNamespace(context.Background(), "acme-test", map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "test"}); err != nil {
		t.Fatal(err)
	}
	createVMBWithBackups(t, "acme-test", fx)
	ensureProject(t)
	createVMR(t, "crossns-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "crossns-restore")
	requireRestoreState(t, getVMR(t, "crossns-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, RestoreReasonBackupNotFound)
	requireNoRestore(t, "crossns-restore")
}

func TestRestoreRenameIsNotSupported(t *testing.T) {
	fx := fixtureOf("rename")
	ensureProject(t)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "rename-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: "rename-copy"})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "rename-restore")
	requireRestoreState(t, getVMR(t, "rename-restore"), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonRenameNotSupported)
	requireNoRestore(t, "rename-restore")
}

func TestRestoreRefusesAnExistingTarget(t *testing.T) {
	setLocation(t, "Available")
	fx := fixtureOf("exists")
	createVM(t, fx.vm, fx.volume)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "exists-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "exists-restore")
	requireRestoreState(t, getVMR(t, "exists-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, RestoreReasonTargetExists)
	requireNoRestore(t, "exists-restore")
}

func TestRestoreReplaceHoldsDeletesThenRestores(t *testing.T) {
	fx := fixtureOf("replace")
	readyForRestore(t, fx)
	createVM(t, fx.vm, fx.volume)
	createKubeVirtObjects(t, fx)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "replace-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	result := reconcileVMR(t, r, projectNamespace, "replace-restore")
	if holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}) != "replace-restore" || holdOf(t, &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Name: fx.volume, Namespace: projectNamespace}}) != "replace-restore" {
		t.Fatal("hold annotation missing")
	}
	requireNoRestore(t, "replace-restore")
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	requireRestoreState(t, getVMR(t, "replace-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
	settleUnderlying(t)
	if left := underlyingCount(t, fx); left != 0 {
		t.Fatalf("%d underlying objects remain", left)
	}
	result = reconcileVMR(t, r, projectNamespace, "replace-restore")
	restore, err := getVelero(t, "Restore", restoreName("replace-restore"))
	if err != nil {
		t.Fatal(err)
	}
	if backup, _, _ := unstructured.NestedString(restore.Object, "spec", "backupName"); backup != fx.backupID {
		t.Fatalf("restore spec %v", restore.Object["spec"])
	}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: VeleroNamespace, Name: restoreName("replace-restore") + "-modifiers"}, &corev1.ConfigMap{}); err != nil {
		t.Fatal(err)
	}
	requireRestoreState(t, getVMR(t, "replace-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonRestoring)
	if result.RequeueAfter != 15*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
}

func TestRestoreReleasesTheHoldOnSuccess(t *testing.T) {
	fx := fixtureOf("success")
	readyForRestore(t, fx)
	createVM(t, fx.vm, fx.volume)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "success-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "success-restore")
	reconcileVMR(t, r, projectNamespace, "success-restore")
	restored := &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "success-restored", Namespace: projectNamespace, Annotations: map[string]string{HoldAnnotation: "success-restore"}}, Spec: v1alpha1.VirtualMachineSpec{InstanceType: "small", RootDisk: v1alpha1.VMRootDisk{SizeGi: 10}}}
	if err := k8sClient.Create(context.Background(), restored); err != nil {
		t.Fatal(err)
	}
	setRestoreStatus(t, "success-restore", map[string]any{"phase": "Completed"})
	reconcileVMR(t, r, projectNamespace, "success-restore")
	for _, obj := range []client.Object{
		&v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}},
		&v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Name: fx.volume, Namespace: projectNamespace}},
		&v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "success-restored", Namespace: projectNamespace}},
	} {
		if hold := holdOf(t, obj); hold != "" {
			t.Fatalf("%s keeps hold %q", obj.GetName(), hold)
		}
	}
	requireRestoreState(t, getVMR(t, "success-restore"), v1alpha1.RestorePhaseCompleted, metav1.ConditionTrue, RestoreReasonCompleted)
}

func TestRestoreReleasesTheHoldOnFailure(t *testing.T) {
	fx := fixtureOf("failure")
	readyForRestore(t, fx)
	createVM(t, fx.vm, fx.volume)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "failure-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "failure-restore")
	reconcileVMR(t, r, projectNamespace, "failure-restore")
	setRestoreStatus(t, "failure-restore", map[string]any{"phase": "PartiallyFailed", "failureReason": "boom"})
	reconcileVMR(t, r, projectNamespace, "failure-restore")
	vmr := getVMR(t, "failure-restore")
	requireRestoreState(t, vmr, v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonFailed)
	if !strings.Contains(vmr.Status.Conditions[0].Message, "boom") {
		t.Fatalf("message %q", vmr.Status.Conditions[0].Message)
	}
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "" {
		t.Fatalf("hold %q", hold)
	}
	if hold := holdOf(t, &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Name: fx.volume, Namespace: projectNamespace}}); hold != "" {
		t.Fatalf("hold %q", hold)
	}
}

func TestRestoreDeletionReleasesTheHold(t *testing.T) {
	fx := fixtureOf("deleted")
	readyForRestore(t, fx)
	createVM(t, fx.vm, fx.volume)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "deleted-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "deleted-restore")
	reconcileVMR(t, r, projectNamespace, "deleted-restore")
	if _, err := getVelero(t, "Restore", restoreName("deleted-restore")); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(context.Background(), getVMR(t, "deleted-restore")); err != nil {
		t.Fatal(err)
	}
	reconcileVMR(t, r, projectNamespace, "deleted-restore")
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "" {
		t.Fatalf("hold %q", hold)
	}
	requireNoRestore(t, "deleted-restore")
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "deleted-restore"}, &v1alpha1.VirtualMachineRestore{})
	requireGone(t, err)
}

func TestHoldHelpers(t *testing.T) {
	input := map[string]string{"a": "b"}
	held := WithHold(input, "r1")
	if len(input) != 1 || held[HoldAnnotation] != "r1" || held["a"] != "b" {
		t.Fatalf("input %v held %v", input, held)
	}
	released := WithoutHold(held, "r1")
	if _, found := released[HoldAnnotation]; found || held[HoldAnnotation] != "r1" {
		t.Fatalf("released %v held %v", released, held)
	}
	if kept := WithoutHold(held, "r2"); kept[HoldAnnotation] != "r1" {
		t.Fatalf("foreign hold removed: %v", kept)
	}
	if WithHold(nil, "r1")[HoldAnnotation] != "r1" {
		t.Fatal("nil annotations")
	}
}

func TestRestoreReplaceNeedsTheVeleroBackup(t *testing.T) {
	fx := fixtureOf("nobackup")
	setLocation(t, "Available")
	createVM(t, fx.vm, fx.volume)
	createKubeVirtObjects(t, fx)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "nobackup-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "nobackup-restore")
	requireRestoreState(t, getVMR(t, "nobackup-restore"), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonBackupNotFound)
	requireUnderlyingIntact(t, fx)
	requireNoRestore(t, "nobackup-restore")
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "" {
		t.Fatalf("hold %q", hold)
	}
}

func TestRestoreReplaceNeedsACompletedVeleroBackup(t *testing.T) {
	fx := fixtureOf("running")
	setLocation(t, "Available")
	createFixtureBackup(t, fx.backupID, fx.vmb, "Deleting", "")
	createVM(t, fx.vm, fx.volume)
	createKubeVirtObjects(t, fx)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "running-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "running-restore")
	requireRestoreState(t, getVMR(t, "running-restore"), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonBackupNotFound)
	requireUnderlyingIntact(t, fx)
}

func TestRestoreReplaceWaitsForTheLocation(t *testing.T) {
	fx := fixtureOf("nolocation")
	setLocation(t, "Unavailable")
	createFixtureBackup(t, fx.backupID, fx.vmb, "Completed", "2026-10-08T02:05:00Z")
	createVM(t, fx.vm, fx.volume)
	createKubeVirtObjects(t, fx)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "nolocation-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	r := newRestoreReconciler()
	result := reconcileVMR(t, r, projectNamespace, "nolocation-restore")
	requireRestoreState(t, getVMR(t, "nolocation-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, ReasonTargetUnavailable)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	requireUnderlyingIntact(t, fx)
	requireNoRestore(t, "nolocation-restore")
	setLocation(t, "Available")
	reconcileVMR(t, r, projectNamespace, "nolocation-restore")
	requireRestoreState(t, getVMR(t, "nolocation-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
}
