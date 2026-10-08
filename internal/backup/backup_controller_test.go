package backup

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const projectNamespace = "acme-prod"

var projectLabels = map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "prod"}

func veleroObject(kind, version, name string, spec, status map[string]any, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "velero.io", Version: version, Kind: kind})
	u.SetNamespace(VeleroNamespace)
	u.SetName(name)
	u.SetLabels(labels)
	if spec != nil {
		u.Object["spec"] = spec
	}
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func vmbVeleroLabels(vmb string) map[string]string {
	return v1alpha1.TenantLabels("VirtualMachineBackup", vmb, "acme", "prod")
}

func createFixtureBackup(t *testing.T, name, vmb, phase, completed string) {
	t.Helper()
	status := map[string]any{"phase": phase}
	if completed != "" {
		status["completionTimestamp"] = completed
	}
	obj := veleroObject("Backup", "v1", name, map[string]any{"storageLocation": LocationName}, status, vmbVeleroLabels(vmb))
	if err := k8sClient.Create(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

func setLocation(t *testing.T, phase string) {
	t.Helper()
	ctx := context.Background()
	removeLocation(t)
	status := map[string]any{"phase": phase}
	spec := map[string]any{"provider": "aws", "objectStorage": map[string]any{"bucket": "bedrock-backups"}}
	if err := k8sClient.Create(ctx, veleroObject("BackupStorageLocation", "v1", LocationName, spec, status, nil)); err != nil {
		t.Fatal(err)
	}
}

func removeLocation(t *testing.T) {
	t.Helper()
	err := k8sClient.Delete(context.Background(), veleroObject("BackupStorageLocation", "v1", LocationName, nil, nil, nil))
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func ensureProject(t *testing.T) {
	t.Helper()
	if err := ensureNamespace(context.Background(), projectNamespace, projectLabels); err != nil {
		t.Fatal(err)
	}
}

func createVM(t *testing.T, name string, volumes ...string) {
	t.Helper()
	ensureProject(t)
	ctx := context.Background()
	vm := &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: projectNamespace}, Spec: v1alpha1.VirtualMachineSpec{InstanceType: "small", RootDisk: v1alpha1.VMRootDisk{SizeGi: 10}}}
	for _, volume := range volumes {
		vm.Spec.Disks = append(vm.Spec.Disks, v1alpha1.VMDisk{VolumeRef: volume})
		v := &v1alpha1.Volume{ObjectMeta: metav1.ObjectMeta{Name: volume, Namespace: projectNamespace}, Spec: v1alpha1.VolumeSpec{SizeGi: 5}}
		if err := k8sClient.Create(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := k8sClient.Create(ctx, vm); err != nil {
		t.Fatal(err)
	}
}

func createVMB(t *testing.T, name string, spec v1alpha1.VirtualMachineBackupSpec) *v1alpha1.VirtualMachineBackup {
	t.Helper()
	ensureProject(t)
	if spec.Target == "" {
		spec.Target = "default"
	}
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: projectNamespace}, Spec: spec}
	if err := k8sClient.Create(context.Background(), vmb); err != nil {
		t.Fatal(err)
	}
	return vmb
}

func newBackupReconciler(now time.Time) *BackupReconciler {
	return &BackupReconciler{Client: k8sClient, Now: func() time.Time { return now }}
}

func reconcileVMB(t *testing.T, r *BackupReconciler, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: projectNamespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func getVMB(t *testing.T, name string) *v1alpha1.VirtualMachineBackup {
	t.Helper()
	vmb := &v1alpha1.VirtualMachineBackup{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: name}, vmb); err != nil {
		t.Fatal(err)
	}
	return vmb
}

func readyOf(vmb *v1alpha1.VirtualMachineBackup) (metav1.ConditionStatus, string) {
	for _, c := range vmb.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady {
			return c.Status, c.Reason
		}
	}
	return "", ""
}

func requireReady(t *testing.T, vmb *v1alpha1.VirtualMachineBackup, status metav1.ConditionStatus, reason string) {
	t.Helper()
	gotStatus, gotReason := readyOf(vmb)
	if gotStatus != status || gotReason != reason {
		t.Fatalf("Ready=%s/%s, want %s/%s", gotStatus, gotReason, status, reason)
	}
}

func listVelero(t *testing.T, kind string, labels map[string]string) []unstructured.Unstructured {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: kind + "List"})
	if err := k8sClient.List(context.Background(), list, client.InNamespace(VeleroNamespace), client.MatchingLabels(labels)); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestBackupOneShotCreatesOneVeleroBackup(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "oneshot-vm", "oneshot-data")
	createVMB(t, "once", v1alpha1.VirtualMachineBackupSpec{VMRef: "oneshot-vm"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "once")
	reconcileVMB(t, r, "once")
	backups := listVelero(t, "Backup", vmbVeleroLabels("once"))
	if len(backups) != 1 || backups[0].GetName() != VeleroName("vmb", projectNamespace, "once") {
		t.Fatalf("backups %v", backups)
	}
	selectors, _, _ := unstructured.NestedSlice(backups[0].Object, "spec", "orLabelSelectors")
	if len(selectors) != 2 {
		t.Fatalf("selectors %v", selectors)
	}
	vmb := getVMB(t, "once")
	if vmb.Status.Phase != v1alpha1.BackupPhaseInProgress {
		t.Fatalf("phase %q", vmb.Status.Phase)
	}
	vm := &v1alpha1.VirtualMachine{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "oneshot-vm"}, vm); err != nil || vm.Labels[BackupVMLabel] != "oneshot-vm" {
		t.Fatalf("vm labels %v %v", vm.Labels, err)
	}
	volume := &v1alpha1.Volume{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "oneshot-data"}, volume); err != nil || volume.Labels[BackupVMLabel] != "oneshot-vm" {
		t.Fatalf("volume labels %v %v", volume.Labels, err)
	}
	if len(vmb.Finalizers) != 1 || vmb.Finalizers[0] != BackupFinalizer {
		t.Fatalf("finalizers %v", vmb.Finalizers)
	}
}

func TestBackupScheduleAppliesTheSchedule(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "sched-vm")
	createVMB(t, "sched", v1alpha1.VirtualMachineBackupSpec{VMRef: "sched-vm", Schedule: "0 2 * * *", Keep: 7})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "sched")
	schedules := listVelero(t, "Schedule", vmbVeleroLabels("sched"))
	if len(schedules) != 1 {
		t.Fatalf("schedules %d", len(schedules))
	}
	if cron, _, _ := unstructured.NestedString(schedules[0].Object, "spec", "schedule"); cron != "0 2 * * *" {
		t.Fatalf("cron %q", cron)
	}
	if len(listVelero(t, "Backup", vmbVeleroLabels("sched"))) != 0 {
		t.Fatal("a schedule must not create a backup")
	}
	vmb := getVMB(t, "sched")
	requireReady(t, vmb, metav1.ConditionTrue, ReasonScheduled)
	vmb.Spec.Schedule = "0 3 * * *"
	if err := k8sClient.Update(context.Background(), vmb); err != nil {
		t.Fatal(err)
	}
	reconcileVMB(t, r, "sched")
	schedules = listVelero(t, "Schedule", vmbVeleroLabels("sched"))
	if cron, _, _ := unstructured.NestedString(schedules[0].Object, "spec", "schedule"); len(schedules) != 1 || cron != "0 3 * * *" {
		t.Fatalf("schedules after update %v", schedules)
	}
}

func TestBackupStatusFromVelero(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "status-vm")
	createVMB(t, "stat", v1alpha1.VirtualMachineBackupSpec{VMRef: "status-vm", Schedule: "0 2 * * *"})
	older := "stat-20261007020000"
	newer := "stat-20261008020000"
	createFixtureBackup(t, older, "stat", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, newer, "stat", "Completed", "2026-10-08T02:05:00Z")
	upload := veleroObject("DataUpload", "v2alpha1", "stat-upload", map[string]any{
		"backupStorageLocation": LocationName, "operationTimeout": "10m", "snapshotType": "CSI", "sourceNamespace": projectNamespace, "sourcePVC": "web-root",
	}, map[string]any{"progress": map[string]any{"totalBytes": int64(1073741824)}}, map[string]string{"velero.io/backup-name": newer})
	if err := k8sClient.Create(context.Background(), upload); err != nil {
		t.Fatal(err)
	}
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "stat")
	vmb := getVMB(t, "stat")
	if len(vmb.Status.Backups) != 2 {
		t.Fatalf("backups %+v", vmb.Status.Backups)
	}
	first := vmb.Status.Backups[0]
	if first.ID != newer || first.SizeBytes != 1073741824 || !first.Time.Time.Equal(time.Date(2026, 10, 8, 2, 5, 0, 0, time.UTC)) {
		t.Fatalf("first %+v", first)
	}
	if vmb.Status.LastBackup == nil || !vmb.Status.LastBackup.Time.Equal(first.Time.Time) || vmb.Status.Phase != v1alpha1.BackupPhaseCompleted {
		t.Fatalf("status %+v", vmb.Status)
	}
	requireReady(t, vmb, metav1.ConditionTrue, ReasonScheduled)
}

func TestBackupWaitsForTheLocation(t *testing.T) {
	createVM(t, "wait-vm")
	createVMB(t, "wait", v1alpha1.VirtualMachineBackupSpec{VMRef: "wait-vm"})
	r := newBackupReconciler(testNow)
	removeLocation(t)
	reconcileVMB(t, r, "wait")
	requireReady(t, getVMB(t, "wait"), metav1.ConditionFalse, ReasonTargetNotConfigured)
	setLocation(t, "Unavailable")
	result := reconcileVMB(t, r, "wait")
	requireReady(t, getVMB(t, "wait"), metav1.ConditionFalse, ReasonTargetUnavailable)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	if len(listVelero(t, "Backup", vmbVeleroLabels("wait"))) != 0 || len(listVelero(t, "Schedule", vmbVeleroLabels("wait"))) != 0 {
		t.Fatal("no Velero object may exist")
	}
}

func TestBackupNeedsTheVirtualMachine(t *testing.T) {
	setLocation(t, "Available")
	createVMB(t, "novm", v1alpha1.VirtualMachineBackupSpec{VMRef: "missing"})
	r := newBackupReconciler(testNow)
	result := reconcileVMB(t, r, "novm")
	requireReady(t, getVMB(t, "novm"), metav1.ConditionFalse, ReasonVirtualMachineNotFound)
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
}

func TestBackupRefusesBadInput(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "bad-vm")
	cases := []struct {
		name   string
		spec   v1alpha1.VirtualMachineBackupSpec
		reason string
	}{
		{"bad-target", v1alpha1.VirtualMachineBackupSpec{VMRef: "bad-vm", Target: "other"}, ReasonTargetNotFound},
		{"bad-freq", v1alpha1.VirtualMachineBackupSpec{VMRef: "bad-vm", Schedule: "* * * * *"}, ReasonScheduleTooFrequent},
		{"bad-keep", v1alpha1.VirtualMachineBackupSpec{VMRef: "bad-vm", Schedule: "0 2 * * *", Keep: 400}, ReasonKeepInvalid},
		{"bad-cron", v1alpha1.VirtualMachineBackupSpec{VMRef: "bad-vm", Schedule: "@daily"}, ReasonScheduleInvalid},
	}
	r := newBackupReconciler(testNow)
	for _, tc := range cases {
		createVMB(t, tc.name, tc.spec)
		reconcileVMB(t, r, tc.name)
		requireReady(t, getVMB(t, tc.name), metav1.ConditionFalse, tc.reason)
		if len(listVelero(t, "Backup", vmbVeleroLabels(tc.name)))+len(listVelero(t, "Schedule", vmbVeleroLabels(tc.name))) != 0 {
			t.Fatalf("%s created Velero objects", tc.name)
		}
	}
}

func TestBackupOutsideAProjectNamespace(t *testing.T) {
	if err := ensureNamespace(context.Background(), "plain", nil); err != nil {
		t.Fatal(err)
	}
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: "stray", Namespace: "plain"}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default"}}
	if err := k8sClient.Create(context.Background(), vmb); err != nil {
		t.Fatal(err)
	}
	r := newBackupReconciler(testNow)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vmb)}); err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.VirtualMachineBackup{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(vmb), got); err != nil {
		t.Fatal(err)
	}
	requireReady(t, got, metav1.ConditionFalse, ReasonNamespaceNotProject)
}

func TestBackupFailedOneShotIsNotRetried(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "fail-vm")
	createVMB(t, "fail", v1alpha1.VirtualMachineBackupSpec{VMRef: "fail-vm"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "fail")
	name := VeleroName("vmb", projectNamespace, "fail")
	backup, err := getVelero(t, "Backup", name)
	if err != nil {
		t.Fatal(err)
	}
	backup.Object["status"] = map[string]any{"phase": "Failed", "completionTimestamp": "2026-10-08T02:05:00Z"}
	if err := k8sClient.Update(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	reconcileVMB(t, r, "fail")
	vmb := getVMB(t, "fail")
	if vmb.Status.Phase != v1alpha1.BackupPhaseFailed {
		t.Fatalf("phase %q", vmb.Status.Phase)
	}
	requireReady(t, vmb, metav1.ConditionFalse, ReasonBackupFailed)
	if err := k8sClient.Delete(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	reconcileVMB(t, r, "fail")
	if len(listVelero(t, "Backup", vmbVeleroLabels("fail"))) != 0 {
		t.Fatal("a failed one-shot backup must not be retried")
	}
	if got := getVMB(t, "fail").Status.Phase; got != v1alpha1.BackupPhaseFailed {
		t.Fatalf("phase after delete %q", got)
	}
}
