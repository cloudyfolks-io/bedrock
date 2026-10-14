package operator

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestBackupKindsValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	backup := &v1alpha1.VirtualMachineBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "web-nightly", Namespace: "default"},
		Spec:       v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Schedule: "0 2 * * *", Keep: 7},
	}
	createValid(t, c, backup)

	now := metav1.NewTime(time.Now())
	backup.Status.Phase = v1alpha1.BackupPhaseCompleted
	backup.Status.LastBackup = &now
	backup.Status.Backups = []v1alpha1.VMBackupRecord{{ID: "web-nightly-20261008", Time: now, SizeBytes: 1 << 30}}
	if err := c.Status().Update(ctx, backup); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	createInvalid(t, c, &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: "no-target", Namespace: "default"}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: "web"}}, "spec.target")
	createInvalid(t, c, &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: "bad-keep", Namespace: "default"}, Spec: v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Keep: -1}}, "spec.keep")

	restore := &v1alpha1.VirtualMachineRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "web-restore", Namespace: "default"},
		Spec:       v1alpha1.VirtualMachineRestoreSpec{BackupRef: "web-nightly", BackupID: "web-nightly-20261008", TargetName: "web-copy"},
	}
	createValid(t, c, restore)
	restore.Status.Phase = v1alpha1.RestorePhaseInProgress
	restore.Status.BackupID = "web-nightly-20261008"
	if err := c.Status().Update(ctx, restore); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	stored := &v1alpha1.VirtualMachineRestore{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(restore), stored); err != nil || stored.Status.BackupID != "web-nightly-20261008" {
		t.Fatalf("status.backupID must be stored: %q %v", stored.Status.BackupID, err)
	}
	restore.Spec.TargetName = "web-other"
	updateInvalid(t, c, restore, "a restore is immutable")

	inPlace := &v1alpha1.VirtualMachineRestore{ObjectMeta: metav1.ObjectMeta{Name: "web-replace", Namespace: "default"}, Spec: v1alpha1.VirtualMachineRestoreSpec{BackupRef: "web-nightly", Replace: true}}
	createValid(t, c, inPlace)

	both := &v1alpha1.VirtualMachineRestore{ObjectMeta: metav1.ObjectMeta{Name: "both", Namespace: "default"}, Spec: v1alpha1.VirtualMachineRestoreSpec{BackupRef: "web-nightly", TargetName: "x", Replace: true}}
	createInvalid(t, c, both, "exactly one of targetName and replace must be set")
	neither := &v1alpha1.VirtualMachineRestore{ObjectMeta: metav1.ObjectMeta{Name: "neither", Namespace: "default"}, Spec: v1alpha1.VirtualMachineRestoreSpec{BackupRef: "web-nightly"}}
	createInvalid(t, c, neither, "exactly one of targetName and replace must be set")
}
