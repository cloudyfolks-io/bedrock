package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	return &BackupReconciler{Client: k8sClient, Now: func() time.Time { return now }, Recorder: record.NewFakeRecorder(20)}
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

var testNow = time.Now().UTC()

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
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "oneshot-vm"}, vm); err != nil || vm.Labels[BackupVMLabel] != "oneshot-vm" || vm.Labels[ExcludeFromBackup] != "true" {
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

func deleteRequestFor(t *testing.T, backupName string) error {
	t.Helper()
	_, err := getVelero(t, "DeleteBackupRequest", VeleroName("del", projectNamespace, backupName))
	return err
}

func TestBackupPrunesBeyondKeep(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "prune-vm")
	createVMB(t, "prune", v1alpha1.VirtualMachineBackupSpec{VMRef: "prune-vm", Schedule: "0 2 * * *", Keep: 2})
	for i, day := range []string{"05", "06", "07", "08"} {
		createFixtureBackup(t, "prune-2026100"+string(rune('1'+i)), "prune", "Completed", "2026-10-"+day+"T02:05:00Z")
	}
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "prune")
	for _, old := range []string{"prune-20261001", "prune-20261002"} {
		if err := deleteRequestFor(t, old); err != nil {
			t.Fatalf("delete request for %s: %v", old, err)
		}
	}
	for _, kept := range []string{"prune-20261003", "prune-20261004"} {
		requireGone(t, deleteRequestFor(t, kept))
	}
	vmb := getVMB(t, "prune")
	if len(vmb.Status.Backups) != 2 || vmb.Status.Backups[0].ID != "prune-20261004" || vmb.Status.Backups[1].ID != "prune-20261003" {
		t.Fatalf("status backups %+v", vmb.Status.Backups)
	}
	reconcileVMB(t, r, "prune")
	if len(getVMB(t, "prune").Status.Backups) != 2 {
		t.Fatal("second reconcile changed the list")
	}
}

func TestBackupPruneIgnoresOneShots(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "keep1-vm")
	createVMB(t, "keep1", v1alpha1.VirtualMachineBackupSpec{VMRef: "keep1-vm", Keep: 1})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "keep1")
	created, err := getVelero(t, "Backup", VeleroName("vmb", projectNamespace, "keep1"))
	if err != nil {
		t.Fatal(err)
	}
	created.Object["status"] = map[string]any{"phase": "Completed", "completionTimestamp": "2026-10-08T02:05:00Z"}
	if err := k8sClient.Update(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	createFixtureBackup(t, "keep1-extra", "keep1", "Completed", "2026-10-07T02:05:00Z")
	reconcileVMB(t, r, "keep1")
	requireGone(t, deleteRequestFor(t, "keep1-extra"))
	requireGone(t, deleteRequestFor(t, created.GetName()))
}

func TestBackupDeletionDeletesTheScheduleAndBackups(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "del-vm")
	createVMB(t, "del", v1alpha1.VirtualMachineBackupSpec{VMRef: "del-vm", Schedule: "0 2 * * *"})
	createFixtureBackup(t, "del-20261007", "del", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, "del-20261008", "del", "Completed", "2026-10-08T02:05:00Z")
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "del")
	if _, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, "del")); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(context.Background(), getVMB(t, "del")); err != nil {
		t.Fatal(err)
	}
	result := reconcileVMB(t, r, "del")
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	_, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, "del"))
	requireGone(t, err)
	for _, id := range []string{"del-20261007", "del-20261008"} {
		if err := deleteRequestFor(t, id); err != nil {
			t.Fatalf("delete request for %s: %v", id, err)
		}
	}
	vmb := getVMB(t, "del")
	if len(vmb.Finalizers) != 1 {
		t.Fatalf("finalizers %v", vmb.Finalizers)
	}
	requireReady(t, vmb, metav1.ConditionFalse, ReasonDeleting)
	for _, id := range []string{"del-20261007", "del-20261008"} {
		backup, err := getVelero(t, "Backup", id)
		if err != nil {
			t.Fatal(err)
		}
		if err := k8sClient.Delete(context.Background(), backup); err != nil {
			t.Fatal(err)
		}
	}
	reconcileVMB(t, r, "del")
	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "del"}, &v1alpha1.VirtualMachineBackup{})
	requireGone(t, err)
}

func TestBackupDeletionTimesOut(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "late-vm")
	createVMB(t, "late", v1alpha1.VirtualMachineBackupSpec{VMRef: "late-vm", Schedule: "0 2 * * *"})
	createFixtureBackup(t, "late-20261008", "late", "Completed", "2026-10-08T02:05:00Z")
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "late")
	if err := k8sClient.Delete(context.Background(), getVMB(t, "late")); err != nil {
		t.Fatal(err)
	}
	recorder := record.NewFakeRecorder(10)
	late := &BackupReconciler{Client: k8sClient, Now: func() time.Time { return time.Now().Add(11 * time.Minute) }, Recorder: recorder}
	reconcileVMB(t, late, "late")
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: "late"}, &v1alpha1.VirtualMachineBackup{})
	requireGone(t, err)
	events := drainEvents(recorder)
	if len(events) != 1 || !strings.Contains(events[0], "BackupsLeftBehind") || !strings.Contains(events[0], "1") {
		t.Fatalf("events %v", events)
	}
}

func TestBackupDeletionWithoutVelero(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(testNow)
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: projectNamespace, Finalizers: []string{BackupFinalizer}, DeletionTimestamp: &now}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default"}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: projectNamespace, Labels: projectLabels}}
	missing := func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "velero.io", Kind: "Schedule"}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmb, namespace).WithStatusSubresource(vmb).WithInterceptorFuncs(interceptor.Funcs{Delete: missing}).Build()
	r := &BackupReconciler{Client: c, Now: func() time.Time { return testNow }, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vmb)}); err != nil {
		t.Fatal(err)
	}
	err := c.Get(context.Background(), client.ObjectKeyFromObject(vmb), &v1alpha1.VirtualMachineBackup{})
	requireGone(t, err)
}

func TestBackupScheduleRejectedByVelero(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "invalid-vm")
	createVMB(t, "invalid", v1alpha1.VirtualMachineBackupSpec{VMRef: "invalid-vm", Schedule: "0 2 * * *"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "invalid")
	requireReady(t, getVMB(t, "invalid"), metav1.ConditionTrue, ReasonScheduled)
	schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, "invalid"))
	if err != nil {
		t.Fatal(err)
	}
	schedule.Object["status"] = map[string]any{"phase": "FailedValidation", "validationErrors": []any{"bad cron"}}
	if err := k8sClient.Update(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	reconcileVMB(t, r, "invalid")
	vmb := getVMB(t, "invalid")
	requireReady(t, vmb, metav1.ConditionFalse, ReasonScheduleInvalid)
	if !strings.Contains(vmb.Status.Conditions[0].Message, "bad cron") {
		t.Fatalf("message %q", vmb.Status.Conditions[0].Message)
	}
}

func TestBackupRetriesAFailedDeleteRequest(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "retry-vm")
	createVMB(t, "retry", v1alpha1.VirtualMachineBackupSpec{VMRef: "retry-vm", Schedule: "0 2 * * *", Keep: 1})
	createFixtureBackup(t, "retry-20261007", "retry", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, "retry-20261008", "retry", "Completed", "2026-10-08T02:05:00Z")
	failed := DeleteBackupRequest(fixtureScope, "retry-20261007")
	failed.Object["status"] = map[string]any{"phase": "Processed", "errors": []any{"object store unreachable"}}
	if err := k8sClient.Create(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	r := newBackupReconciler(testNow)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: projectNamespace, Name: "retry"}})
	if err == nil || !strings.Contains(err.Error(), "retry-20261007") {
		t.Fatalf("a failed request must give an error, got %v", err)
	}
	if err := deleteRequestFor(t, "retry-20261007"); !apierrors.IsNotFound(err) {
		t.Fatalf("the failed request must be removed: %v", err)
	}
	reconcileVMB(t, r, "retry")
	if err := deleteRequestFor(t, "retry-20261007"); err != nil {
		t.Fatalf("the request must be created again: %v", err)
	}
}

func TestBackupNamesAreLimitedTo63Characters(t *testing.T) {
	ensureProject(t)
	ctx := context.Background()
	long := strings.Repeat("a", 64)
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: long, Namespace: projectNamespace}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default"}}
	if err := k8sClient.Create(ctx, vmb); !apierrors.IsInvalid(err) {
		t.Fatalf("a 64 character backup name must be invalid, got %v", err)
	}
	vmr := &v1alpha1.VirtualMachineRestore{ObjectMeta: metav1.ObjectMeta{Name: long, Namespace: projectNamespace}, Spec: v1alpha1.VirtualMachineRestoreSpec{BackupRef: "daily", Replace: true}}
	if err := k8sClient.Create(ctx, vmr); !apierrors.IsInvalid(err) {
		t.Fatalf("a 64 character restore name must be invalid, got %v", err)
	}
	vmb.Name = strings.Repeat("b", 63)
	if err := k8sClient.Create(ctx, vmb); err != nil {
		t.Fatalf("a 63 character name must be valid: %v", err)
	}
}
