package backup

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

	createFixtureBackup(t, "orphan-20261007", "orphan", "Completed", "2026-10-07T02:05:00Z")
	createFixtureBackup(t, "orphan-20261008", "orphan", "Completed", "2026-10-08T02:05:00Z")
	createFixtureBackup(t, "orphan-20261009", "orphan", "Completed", "2026-10-09T02:05:00Z")
	reconcileVMB(t, r, "orphan")
	requireDeleteRequests(t, map[string]bool{"orphan-20261005": false, "orphan-20261006": false, "orphan-20261007": false})

	createVM(t, "orphan-vm")
	reconcileVMB(t, r, "orphan")
	if scheduleIsPaused(t, "orphan") {
		t.Fatal("the schedule must run again when the VM exists again")
	}
	requireDeleteRequests(t, map[string]bool{"orphan-20261005": true, "orphan-20261006": true, "orphan-20261007": true, "orphan-20261008": false, "orphan-20261009": false})
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
