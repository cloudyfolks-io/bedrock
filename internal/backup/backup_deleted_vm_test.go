package backup

import (
	"context"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func deleteVM(t *testing.T, name string) {
	t.Helper()
	vm := &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: projectNamespace}}
	if err := k8sClient.Delete(context.Background(), vm); err != nil {
		t.Fatal(err)
	}
}

func scheduleIsPaused(t *testing.T, vmb string) bool {
	t.Helper()
	schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, vmb))
	if err != nil {
		t.Fatal(err)
	}
	paused, _, _ := unstructured.NestedBool(schedule.Object, "spec", "paused")
	return paused
}

func requireDeleteRequests(t *testing.T, want map[string]bool) {
	t.Helper()
	for id, wanted := range want {
		err := deleteRequestFor(t, id)
		if wanted && err != nil {
			t.Fatalf("delete request for %s: %v", id, err)
		}
		if !wanted {
			requireGone(t, err)
		}
	}
}

func requireVMBIntact(t *testing.T, name string, backups ...string) {
	t.Helper()
	vmb := getVMB(t, name)
	if !vmb.DeletionTimestamp.IsZero() || len(vmb.OwnerReferences) != 0 {
		t.Fatalf("the backup %s is deleting or owned: %+v", name, vmb.ObjectMeta)
	}
	for _, id := range backups {
		backup, err := getVelero(t, "Backup", id)
		if err != nil {
			t.Fatalf("velero backup %s: %v", id, err)
		}
		if !backup.GetDeletionTimestamp().IsZero() || len(backup.GetOwnerReferences()) != 0 {
			t.Fatalf("velero backup %s is deleting or owned", id)
		}
	}
}

func TestDeletingTheVMKeepsItsBackupsAndPausesTheSchedule(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "orphan-vm")
	createVMB(t, "orphan", v1alpha1.VirtualMachineBackupSpec{VMRef: "orphan-vm", Schedule: "0 2 * * *", Keep: 2})
	createFixtureBackup(t, "orphan-20261005", "orphan", "Completed", "2026-10-05T02:05:00Z")
	createFixtureBackup(t, "orphan-20261006", "orphan", "Completed", "2026-10-06T02:05:00Z")
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "orphan")
	if scheduleIsPaused(t, "orphan") {
		t.Fatal("the schedule of an existing VM must run")
	}

	deleteVM(t, "orphan-vm")
	reconcileVMB(t, r, "orphan")
	requireReady(t, getVMB(t, "orphan"), metav1.ConditionFalse, ReasonPaused)
	requireVMBIntact(t, "orphan", "orphan-20261005", "orphan-20261006")
	if len(getVMB(t, "orphan").Status.Backups) != 2 {
		t.Fatalf("the backup list changed: %+v", getVMB(t, "orphan").Status.Backups)
	}
	if !scheduleIsPaused(t, "orphan") {
		t.Fatal("the schedule of a deleted VM must be paused")
	}

	extras := []string{"orphan-20261007", "orphan-20261008", "orphan-20261009"}
	for _, id := range extras {
		createFixtureBackup(t, id, "orphan", "Completed", "2026-10-07T02:05:00Z")
	}
	reconcileVMB(t, r, "orphan")
	requireDeleteRequests(t, map[string]bool{"orphan-20261005": false, "orphan-20261006": false, "orphan-20261007": false})
	for _, id := range extras {
		backup, err := getVelero(t, "Backup", id)
		if err != nil {
			t.Fatal(err)
		}
		if err := k8sClient.Delete(context.Background(), backup); err != nil {
			t.Fatal(err)
		}
	}

	createVM(t, "orphan-vm")
	reconcileVMB(t, r, "orphan")
	if scheduleIsPaused(t, "orphan") {
		t.Fatal("the schedule must run again when the VM exists again")
	}
	requireDeleteRequests(t, map[string]bool{"orphan-20261005": false, "orphan-20261006": false})
	requireVMBIntact(t, "orphan", "orphan-20261005", "orphan-20261006")
}

func scheduleSkipsImmediately(t *testing.T, vmb string) bool {
	t.Helper()
	schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, vmb))
	if err != nil {
		t.Fatal(err)
	}
	skips, _, _ := unstructured.NestedBool(schedule.Object, "spec", "skipImmediately")
	return skips
}

func veleroConsumesTheSkip(t *testing.T, vmb string) {
	t.Helper()
	schedule, err := getVelero(t, "Schedule", VeleroName("vmb", projectNamespace, vmb))
	if err != nil {
		t.Fatal(err)
	}
	patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"skipImmediately":false}}`))
	if err := k8sClient.Patch(context.Background(), schedule, patch, client.FieldOwner("velero")); err != nil {
		t.Fatal(err)
	}
}

func TestResumingTheScheduleSkipsTheOverdueBackup(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "resume-vm")
	createVMB(t, "resume", v1alpha1.VirtualMachineBackupSpec{VMRef: "resume-vm", Schedule: "0 2 * * *"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "resume")
	if scheduleSkipsImmediately(t, "resume") {
		t.Fatal("a schedule that never paused must not skip")
	}

	deleteVM(t, "resume-vm")
	reconcileVMB(t, r, "resume")
	if !scheduleIsPaused(t, "resume") || scheduleSkipsImmediately(t, "resume") {
		t.Fatal("a paused schedule has nothing to skip")
	}

	createVM(t, "resume-vm")
	reconcileVMB(t, r, "resume")
	if scheduleIsPaused(t, "resume") || !scheduleSkipsImmediately(t, "resume") {
		t.Fatal("the resuming apply must unpause the schedule and skip the overdue backup")
	}
	reconcileVMB(t, r, "resume")
	if !scheduleSkipsImmediately(t, "resume") {
		t.Fatal("the skip stays until Velero has used it")
	}

	veleroConsumesTheSkip(t, "resume")
	reconcileVMB(t, r, "resume")
	if scheduleSkipsImmediately(t, "resume") {
		t.Fatal("a later apply must not skip a real tick of the schedule")
	}
}

func TestDeletingTheVMKeepsAOneShotBackup(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "shot-vm")
	createVMB(t, "shot", v1alpha1.VirtualMachineBackupSpec{VMRef: "shot-vm"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "shot")
	id := VeleroName("vmb", projectNamespace, "shot")

	deleteVM(t, "shot-vm")
	reconcileVMB(t, r, "shot")
	requireReady(t, getVMB(t, "shot"), metav1.ConditionFalse, ReasonVirtualMachineNotFound)
	requireVMBIntact(t, "shot", id)
	if len(listVelero(t, "Schedule", vmbVeleroLabels("shot"))) != 0 {
		t.Fatal("a one-shot backup needs no schedule")
	}
}

func TestRestoreHoldPausesTheScheduleAndKeepsTheBackups(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "held-vm")
	createVMB(t, "held", v1alpha1.VirtualMachineBackupSpec{VMRef: "held-vm", Schedule: "0 2 * * *", Keep: 1})
	createFixtureBackup(t, "held-20261007", "held", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, "held-20261008", "held", "Completed", "2026-10-08T02:05:00Z")
	updateVM(t, "held-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: "some-restore"} })
	r := newBackupReconciler(testNow)

	reconcileVMB(t, r, "held")
	if !scheduleIsPaused(t, "held") {
		t.Fatal("the schedule of a held VM must be paused")
	}
	requireDeleteRequests(t, map[string]bool{"held-20261007": false, "held-20261008": false})

	updateVM(t, "held-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = nil })
	reconcileVMB(t, r, "held")
	if scheduleIsPaused(t, "held") {
		t.Fatal("the schedule must run again when the hold ends")
	}
	requireDeleteRequests(t, map[string]bool{"held-20261007": true, "held-20261008": false})
}

func TestPruneKeepsTheBackupThatARestoreNames(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "pin-vm")
	createVMB(t, "pin", v1alpha1.VirtualMachineBackupSpec{VMRef: "pin-vm", Schedule: "0 2 * * *", Keep: 1})
	createFixtureBackup(t, "pin-20261007", "pin", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, "pin-20261008", "pin", "Completed", "2026-10-08T02:05:00Z")
	createFixtureBackup(t, "pin-20261009", "pin", "Completed", "2026-10-09T02:05:00Z")
	createVMR(t, "pin-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: "pin", BackupID: "pin-20261007", Replace: true})
	r := newBackupReconciler(testNow)

	reconcileVMB(t, r, "pin")
	requireDeleteRequests(t, map[string]bool{"pin-20261007": false, "pin-20261008": true, "pin-20261009": false})
	ids := []string{}
	for _, record := range getVMB(t, "pin").Status.Backups {
		ids = append(ids, record.ID)
	}
	if !slices.Equal(ids, []string{"pin-20261009", "pin-20261007"}) {
		t.Fatalf("status backups %v", ids)
	}

	vmr := &v1alpha1.VirtualMachineRestore{ObjectMeta: metav1.ObjectMeta{Name: "pin-restore", Namespace: projectNamespace}}
	if err := k8sClient.Delete(context.Background(), vmr); err != nil {
		t.Fatal(err)
	}
	reconcileVMB(t, r, "pin")
	requireDeleteRequests(t, map[string]bool{"pin-20261007": true})
}

func TestAnEmptyHoldStillPausesTheSchedule(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "emptyhold-vm")
	createVMB(t, "emptyhold", v1alpha1.VirtualMachineBackupSpec{VMRef: "emptyhold-vm", Schedule: "0 2 * * *"})
	updateVM(t, "emptyhold-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: ""} })
	reconcileVMB(t, newBackupReconciler(testNow), "emptyhold")
	if !scheduleIsPaused(t, "emptyhold") {
		t.Fatal("compute treats the key as a hold, so the schedule must pause")
	}
}

func TestPauseHappensWhileTheLocationIsUnavailable(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "away-vm")
	createVMB(t, "away", v1alpha1.VirtualMachineBackupSpec{VMRef: "away-vm", Schedule: "0 2 * * *"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "away")
	setLocation(t, "Unavailable")

	reconcileVMB(t, r, "away")
	requireReady(t, getVMB(t, "away"), metav1.ConditionFalse, ReasonTargetUnavailable)
	if scheduleIsPaused(t, "away") {
		t.Fatal("a VM that exists and is free keeps its schedule")
	}

	updateVM(t, "away-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: "some-restore"} })
	reconcileVMB(t, r, "away")
	if !scheduleIsPaused(t, "away") {
		t.Fatal("a held VM pauses the schedule although the location is unavailable")
	}

	updateVM(t, "away-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = nil })
	setLocation(t, "Available")
	reconcileVMB(t, r, "away")
	if scheduleIsPaused(t, "away") {
		t.Fatal("the schedule must run again")
	}

	setLocation(t, "Unavailable")
	deleteVM(t, "away-vm")
	reconcileVMB(t, r, "away")
	if !scheduleIsPaused(t, "away") {
		t.Fatal("a deleted VM pauses the schedule although the location is unavailable")
	}
}

func requireReadyMessage(t *testing.T, vmb *v1alpha1.VirtualMachineBackup, reason string, parts ...string) {
	t.Helper()
	for _, condition := range vmb.Status.Conditions {
		if condition.Type != v1alpha1.ConditionReady {
			continue
		}
		if condition.Status != metav1.ConditionFalse || condition.Reason != reason {
			t.Fatalf("Ready=%s/%s, want False/%s", condition.Status, condition.Reason, reason)
		}
		for _, part := range parts {
			if !strings.Contains(condition.Message, part) {
				t.Fatalf("message %q lacks %q", condition.Message, part)
			}
		}
		return
	}
	t.Fatal("no Ready condition")
}

func TestPausedScheduleShowsWhyItIsPaused(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "why-vm")
	createVMB(t, "why", v1alpha1.VirtualMachineBackupSpec{VMRef: "why-vm", Schedule: "0 2 * * *"})
	r := newBackupReconciler(testNow)
	reconcileVMB(t, r, "why")
	requireReady(t, getVMB(t, "why"), metav1.ConditionTrue, ReasonScheduled)

	updateVM(t, "why-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: "some-restore"} })
	reconcileVMB(t, r, "why")
	requireReadyMessage(t, getVMB(t, "why"), ReasonPaused, "paused", "some-restore")

	updateVM(t, "why-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = nil })
	reconcileVMB(t, r, "why")
	requireReady(t, getVMB(t, "why"), metav1.ConditionTrue, ReasonScheduled)

	deleteVM(t, "why-vm")
	reconcileVMB(t, r, "why")
	requireReadyMessage(t, getVMB(t, "why"), ReasonPaused, "paused", "why-vm")
}

func TestScheduleOfAMissingVMWithoutASchedulePausesNothing(t *testing.T) {
	setLocation(t, "Available")
	createVMB(t, "never", v1alpha1.VirtualMachineBackupSpec{VMRef: "never-vm", Schedule: "0 2 * * *"})
	reconcileVMB(t, newBackupReconciler(testNow), "never")
	requireReady(t, getVMB(t, "never"), metav1.ConditionFalse, ReasonVirtualMachineNotFound)
	if len(listVelero(t, "Schedule", vmbVeleroLabels("never"))) != 0 {
		t.Fatal("a schedule of a missing VM must not be created")
	}
}

func TestOneShotWaitsWhileARestoreHoldsTheVM(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "waits-vm")
	updateVM(t, "waits-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: "some-restore"} })
	createVMB(t, "waits", v1alpha1.VirtualMachineBackupSpec{VMRef: "waits-vm"})
	r := newBackupReconciler(testNow)

	reconcileVMB(t, r, "waits")
	requireReadyMessage(t, getVMB(t, "waits"), ReasonPaused, "waits", "some-restore")
	if len(listVelero(t, "Backup", vmbVeleroLabels("waits"))) != 0 {
		t.Fatal("a half-restored VM must not be backed up")
	}

	updateVM(t, "waits-vm", func(vm *v1alpha1.VirtualMachine) { vm.Annotations = nil })
	reconcileVMB(t, r, "waits")
	if len(listVelero(t, "Backup", vmbVeleroLabels("waits"))) != 1 {
		t.Fatal("the backup must start when the hold ends")
	}
	requireReady(t, getVMB(t, "waits"), metav1.ConditionFalse, ReasonInProgress)
}

func setVMRPhase(t *testing.T, name, phase string) {
	t.Helper()
	vmr := getVMR(t, name)
	vmr.Status.Phase = phase
	if err := k8sClient.Status().Update(context.Background(), vmr); err != nil {
		t.Fatal(err)
	}
}

func TestPruneIgnoresACompletedRestoreButKeepsAFailedOne(t *testing.T) {
	setLocation(t, "Available")
	createVM(t, "phase-vm")
	createVMB(t, "phase", v1alpha1.VirtualMachineBackupSpec{VMRef: "phase-vm", Schedule: "0 2 * * *", Keep: 1})
	createFixtureBackup(t, "phase-20261007", "phase", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, "phase-20261008", "phase", "Completed", "2026-10-08T02:05:00Z")
	createFixtureBackup(t, "phase-20261009", "phase", "Completed", "2026-10-09T02:05:00Z")
	createVMR(t, "phase-done", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: "phase", BackupID: "phase-20261007", Replace: true})
	createVMR(t, "phase-failed", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: "phase", BackupID: "phase-20261008", Replace: true})
	setVMRPhase(t, "phase-done", v1alpha1.RestorePhaseCompleted)
	setVMRPhase(t, "phase-failed", v1alpha1.RestorePhaseFailed)

	reconcileVMB(t, newBackupReconciler(testNow), "phase")
	requireDeleteRequests(t, map[string]bool{"phase-20261007": true, "phase-20261008": false, "phase-20261009": false})
}
