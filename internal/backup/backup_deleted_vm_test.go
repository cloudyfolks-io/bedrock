package backup

import (
	"context"
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
	requireReady(t, getVMB(t, "orphan"), metav1.ConditionFalse, ReasonVirtualMachineNotFound)
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
