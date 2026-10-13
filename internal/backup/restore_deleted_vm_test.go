package backup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func snapshotFor(fx restoreFixture) string {
	return `{"apiVersion":"bedrock.cloudyfolks.io/v1alpha1","kind":"VirtualMachine","metadata":{"name":"` + fx.vm + `","namespace":"` + projectNamespace +
		`","labels":{"velero.io/exclude-from-backup":"true","team":"dropped"},"annotations":{"bedrock.cloudyfolks.io/restore-hold":"forged","bedrock.cloudyfolks.io/future":"dropped"}},` +
		`"spec":{"instanceType":"small","rootDisk":{"sizeGi":10},"disks":[{"volumeRef":"` + fx.volume + `"}],"running":true}}`
}

func annotateBackup(t *testing.T, name string, annotations map[string]string) {
	t.Helper()
	backup, err := getVelero(t, "Backup", name)
	if err != nil {
		t.Fatal(err)
	}
	backup.SetAnnotations(annotations)
	if err := k8sClient.Update(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
}

func prepareDeletedVM(t *testing.T, fx restoreFixture, snapshot string) {
	t.Helper()
	readyForRestore(t, fx)
	if snapshot != "" {
		annotateBackup(t, fx.backupID, map[string]string{VMSnapshotAnnotation: snapshot})
	}
	ensureProject(t)
	createVMBWithBackups(t, projectNamespace, fx)
}

func getBedrockVM(t *testing.T, name string) (*v1alpha1.VirtualMachine, error) {
	t.Helper()
	vm := &v1alpha1.VirtualMachine{}
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: name}, vm)
	return vm, err
}

type restoreProbe struct {
	vm            types.NamespacedName
	holdAtRestore string
	vmCreates     int
}

func appliedKind(obj runtime.ApplyConfiguration) string {
	named, ok := obj.(interface{ GetKind() string })
	if !ok {
		return ""
	}
	return named.GetKind()
}

func probedRestoreReconciler(t *testing.T, probe *restoreProbe, failApply func(kind string) error) *RestoreReconciler {
	t.Helper()
	base, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	probed := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, isVM := obj.(*v1alpha1.VirtualMachine); isVM {
				probe.vmCreates++
			}
			return c.Create(ctx, obj, opts...)
		},
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			kind := appliedKind(obj)
			if kind == "Restore" {
				vm := &v1alpha1.VirtualMachine{}
				if err := c.Get(ctx, probe.vm, vm); err == nil {
					probe.holdAtRestore = vm.Annotations[HoldAnnotation]
				}
			}
			if failApply != nil {
				if err := failApply(kind); err != nil {
					return err
				}
			}
			return c.Apply(ctx, obj, opts...)
		},
	})
	return &RestoreReconciler{Client: probed, Now: func() time.Time { return testNow }}
}

func TestRestoreOfADeletedVMCreatesTheHeldVMBeforeTheVeleroRestore(t *testing.T) {
	fx := fixtureOf("recreate")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createVMR(t, "recreate-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	probe := &restoreProbe{vm: types.NamespacedName{Namespace: projectNamespace, Name: fx.vm}}
	r := probedRestoreReconciler(t, probe, nil)

	reconcileVMR(t, r, projectNamespace, "recreate-restore")

	if probe.holdAtRestore != "recreate-restore" || probe.vmCreates != 1 {
		t.Fatalf("hold %q and %d creations when Velero was asked to restore", probe.holdAtRestore, probe.vmCreates)
	}
	if _, err := getVelero(t, "Restore", restoreName("recreate-restore")); err != nil {
		t.Fatal(err)
	}
	vm, err := getBedrockVM(t, fx.vm)
	if err != nil {
		t.Fatal(err)
	}
	want := v1alpha1.VirtualMachineSpec{InstanceType: "small", RootDisk: v1alpha1.VMRootDisk{SizeGi: 10}, Disks: []v1alpha1.VMDisk{{VolumeRef: fx.volume}}, Running: true}
	if !reflect.DeepEqual(vm.Spec, want) || vm.Labels[ExcludeFromBackup] != "true" || vm.Labels["team"] != "" || vm.Annotations["bedrock.cloudyfolks.io/future"] != "" || vm.Annotations[HoldAnnotation] != "recreate-restore" {
		t.Fatalf("restored VM %+v", vm.ObjectMeta)
	}
	requireRestoreState(t, getVMR(t, "recreate-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonRestoring)

	setRestoreStatus(t, "recreate-restore", map[string]any{"phase": "Completed"})
	reconcileVMR(t, r, projectNamespace, "recreate-restore")
	requireRestoreState(t, getVMR(t, "recreate-restore"), v1alpha1.RestorePhaseCompleted, metav1.ConditionTrue, RestoreReasonCompleted)
	after, err := getBedrockVM(t, fx.vm)
	if err != nil {
		t.Fatal(err)
	}
	if after.UID != vm.UID || after.Annotations[HoldAnnotation] != "" || after.Labels[ExcludeFromBackup] != "true" || probe.vmCreates != 1 {
		t.Fatalf("after the restore: uid %s/%s, annotations %v, %d creations", vm.UID, after.UID, after.Annotations, probe.vmCreates)
	}
}

func TestRestoreOfADeletedVMResumesAfterAPartialStart(t *testing.T) {
	fx := fixtureOf("resume")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createVMR(t, "resume-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	vmKey := types.NamespacedName{Namespace: projectNamespace, Name: fx.vm}
	firstProbe := &restoreProbe{vm: vmKey}
	broken := probedRestoreReconciler(t, firstProbe, func(kind string) error {
		if kind == "Restore" {
			return errors.New("the API server is not reachable")
		}
		return nil
	})
	if _, err := broken.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: projectNamespace, Name: "resume-restore"}}); err == nil {
		t.Fatal("the failed start must give an error")
	}
	created, err := getBedrockVM(t, fx.vm)
	if err != nil || created.Annotations[HoldAnnotation] != "resume-restore" || firstProbe.vmCreates != 1 {
		t.Fatalf("after the failed start: %v, %d creations, %+v", err, firstProbe.vmCreates, created.ObjectMeta)
	}
	_, err = getVelero(t, "Restore", restoreName("resume-restore"))
	requireGone(t, err)

	secondProbe := &restoreProbe{vm: vmKey}
	healthy := probedRestoreReconciler(t, secondProbe, nil)
	for range 3 {
		reconcileVMR(t, healthy, projectNamespace, "resume-restore")
	}
	resumed, err := getBedrockVM(t, fx.vm)
	if err != nil || resumed.UID != created.UID || resumed.Annotations[HoldAnnotation] != "resume-restore" || secondProbe.vmCreates != 0 {
		t.Fatalf("after the retries: %v, %d creations, uid %s/%s, %+v", err, secondProbe.vmCreates, created.UID, resumed.UID, resumed.ObjectMeta)
	}
	if _, err := getVelero(t, "Restore", restoreName("resume-restore")); err != nil {
		t.Fatal(err)
	}
	requireRestoreState(t, getVMR(t, "resume-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonRestoring)
}

func TestRestoreOfADeletedVMNeedsTheSpecInTheBackup(t *testing.T) {
	fx := fixtureOf("nospec")
	prepareDeletedVM(t, fx, "")
	createVMR(t, "nospec-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, "nospec-restore")
	requireRestoreState(t, getVMR(t, "nospec-restore"), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonSpecMissing)
	if _, err := getBedrockVM(t, fx.vm); !apierrors.IsNotFound(err) {
		t.Fatalf("no VM may be created, got %v", err)
	}
	requireNoRestore(t, "nospec-restore")
}

func TestRestoreOfADeletedVMRefusesAnInvalidSpec(t *testing.T) {
	cases := map[string]func(restoreFixture) string{
		"badjson": func(restoreFixture) string { return "not json" },
		"badname": func(fx restoreFixture) string {
			return strings.Replace(snapshotFor(fx), `"name":"`+fx.vm+`"`, `"name":"other"`, 1)
		},
		"badspec": func(fx restoreFixture) string {
			return strings.Replace(snapshotFor(fx), `"instanceType":"small"`, `"instanceType":""`, 1)
		},
	}
	for name, build := range cases {
		fx := fixtureOf(name)
		prepareDeletedVM(t, fx, build(fx))
		createVMR(t, name+"-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
		reconcileVMR(t, newRestoreReconciler(), projectNamespace, name+"-restore")
		requireRestoreState(t, getVMR(t, name+"-restore"), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonSpecInvalid)
		if _, err := getBedrockVM(t, fx.vm); !apierrors.IsNotFound(err) {
			t.Fatalf("%s: no VM may be created, got %v", name, err)
		}
		requireNoRestore(t, name+"-restore")
	}
}

func TestRestoreOfADeletedVMWaitsForTheOldObjects(t *testing.T) {
	fx := fixtureOf("leftover")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createKubeVirtObjects(t, fx)
	createVMR(t, "leftover-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	r := newRestoreReconciler()

	result := reconcileVMR(t, r, projectNamespace, "leftover-restore")
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	requireRestoreState(t, getVMR(t, "leftover-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
	requireNoRestore(t, "leftover-restore")
	requireUnderlyingIntact(t, fx)
	if vm, err := getBedrockVM(t, fx.vm); err != nil || vm.Annotations[HoldAnnotation] != "leftover-restore" {
		t.Fatalf("the VM must exist with the hold: %v %v", err, vm.Annotations)
	}

	for _, list := range underlyingLists {
		if _, err := r.removeAll(context.Background(), list, projectNamespace, v1alpha1.TenantLabels("VirtualMachine", fx.vm, "acme", "prod")); err != nil {
			t.Fatal(err)
		}
	}
	settleUnderlying(t)
	reconcileVMR(t, r, projectNamespace, "leftover-restore")
	if _, err := getVelero(t, "Restore", restoreName("leftover-restore")); err != nil {
		t.Fatal(err)
	}
	requireRestoreState(t, getVMR(t, "leftover-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonRestoring)
}

func TestRestoreOfADeletedVMRefusesAVMThatAnotherRestoreHolds(t *testing.T) {
	fx := fixtureOf("foreignhold")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createVM(t, fx.vm)
	updateVM(t, fx.vm, func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: "someone-else"} })
	createVMR(t, "foreignhold-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, "foreignhold-restore")
	requireRestoreState(t, getVMR(t, "foreignhold-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, RestoreReasonTargetExists)
	requireNoRestore(t, "foreignhold-restore")
}

func TestRestoreOfADeletedVMKeepsTheHoldWhileTheTargetIsUnavailable(t *testing.T) {
	fx := fixtureOf("flapping")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createKubeVirtObjects(t, fx)
	createVMR(t, "flapping-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	r := newRestoreReconciler()
	reconcileVMR(t, r, projectNamespace, "flapping-restore")
	created, err := getBedrockVM(t, fx.vm)
	if err != nil {
		t.Fatal(err)
	}

	setLocation(t, "Unavailable")
	reconcileVMR(t, r, projectNamespace, "flapping-restore")
	requireRestoreState(t, getVMR(t, "flapping-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, ReasonTargetUnavailable)
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "flapping-restore" {
		t.Fatalf("hold %q", hold)
	}

	setLocation(t, "Available")
	for _, list := range underlyingLists {
		if _, err := r.removeAll(context.Background(), list, projectNamespace, v1alpha1.TenantLabels("VirtualMachine", fx.vm, "acme", "prod")); err != nil {
			t.Fatal(err)
		}
	}
	settleUnderlying(t)
	reconcileVMR(t, r, projectNamespace, "flapping-restore")
	requireRestoreState(t, getVMR(t, "flapping-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonRestoring)
	resumed, err := getBedrockVM(t, fx.vm)
	if err != nil || resumed.UID != created.UID {
		t.Fatalf("the VM was created again: %v %s/%s", err, created.UID, resumed.UID)
	}
}

func TestRestoreReplaceChecksTheCopyOfTheVM(t *testing.T) {
	cases := map[string]func(restoreFixture) string{
		"replacename": func(fx restoreFixture) string {
			return strings.Replace(snapshotFor(fx), `"name":"`+fx.vm+`"`, `"name":"other"`, 1)
		},
		"replacespace": func(fx restoreFixture) string {
			return strings.Replace(snapshotFor(fx), `"namespace":"`+projectNamespace+`"`, `"namespace":"other"`, 1)
		},
	}
	for name, build := range cases {
		fx := fixtureOf(name)
		readyForRestore(t, fx)
		annotateBackup(t, fx.backupID, map[string]string{VMSnapshotAnnotation: build(fx)})
		createVM(t, fx.vm, fx.volume)
		createKubeVirtObjects(t, fx)
		createVMBWithBackups(t, projectNamespace, fx)
		createVMR(t, name+"-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
		reconcileVMR(t, newRestoreReconciler(), projectNamespace, name+"-restore")
		requireRestoreState(t, getVMR(t, name+"-restore"), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonSpecInvalid)
		requireUnderlyingIntact(t, fx)
		requireNoRestore(t, name+"-restore")
		if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "" {
			t.Fatalf("%s: hold %q", name, hold)
		}
	}
}

func TestRestoreReplaceAcceptsTheCopyOfTheSameVM(t *testing.T) {
	fx := fixtureOf("replacesame")
	readyForRestore(t, fx)
	annotateBackup(t, fx.backupID, map[string]string{VMSnapshotAnnotation: snapshotFor(fx)})
	createVM(t, fx.vm, fx.volume)
	createKubeVirtObjects(t, fx)
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "replacesame-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, "replacesame-restore")
	requireRestoreState(t, getVMR(t, "replacesame-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
}

func TestRestoreReplaceRefusesAVMThatAnotherRestoreHolds(t *testing.T) {
	fx := fixtureOf("busy")
	readyForRestore(t, fx)
	createVM(t, fx.vm, fx.volume)
	createKubeVirtObjects(t, fx)
	updateVM(t, fx.vm, func(vm *v1alpha1.VirtualMachine) { vm.Annotations = map[string]string{HoldAnnotation: "someone-else"} })
	createVMBWithBackups(t, projectNamespace, fx)
	createVMR(t, "busy-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, Replace: true})
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, "busy-restore")
	requireRestoreState(t, getVMR(t, "busy-restore"), v1alpha1.RestorePhasePending, metav1.ConditionFalse, RestoreReasonTargetBusy)
	requireNoRestore(t, "busy-restore")
	requireUnderlyingIntact(t, fx)
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "someone-else" {
		t.Fatalf("hold %q", hold)
	}
}

func TestRestoreOfADeletedVMReadsFreshWhenTheCacheIsStale(t *testing.T) {
	fx := fixtureOf("stale")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createVMR(t, "stale-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	broken := probedRestoreReconciler(t, &restoreProbe{vm: types.NamespacedName{Namespace: projectNamespace, Name: fx.vm}}, func(kind string) error {
		if kind == "Restore" {
			return errors.New("the API server is not reachable")
		}
		return nil
	})
	if _, err := broken.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: projectNamespace, Name: "stale-restore"}}); err == nil {
		t.Fatal("the failed start must give an error")
	}
	created, err := getBedrockVM(t, fx.vm)
	if err != nil {
		t.Fatal(err)
	}

	base, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	stale := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isVM := obj.(*v1alpha1.VirtualMachine); isVM {
				return apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "virtualmachines"}, key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r := &RestoreReconciler{Client: stale, APIReader: k8sClient, Now: func() time.Time { return testNow }}
	reconcileVMR(t, r, projectNamespace, "stale-restore")
	requireRestoreState(t, getVMR(t, "stale-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonRestoring)
	resumed, err := getBedrockVM(t, fx.vm)
	if err != nil || resumed.UID != created.UID {
		t.Fatalf("the VM changed: %v", err)
	}
}

func startedRestoreOfADeletedVM(t *testing.T, prefix string) (restoreFixture, string) {
	t.Helper()
	fx := fixtureOf(prefix)
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createKubeVirtObjects(t, fx)
	name := prefix + "-restore"
	createVMR(t, name, projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, name)
	requireRestoreState(t, getVMR(t, name), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != name {
		t.Fatalf("hold %q", hold)
	}
	return fx, name
}

func requireFailedAndReleased(t *testing.T, fx restoreFixture, name string) {
	t.Helper()
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, name)
	requireRestoreState(t, getVMR(t, name), v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, RestoreReasonBackupNotFound)
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "" {
		t.Fatalf("hold %q", hold)
	}
}

func TestRestoreOfADeletedVMFailsWhenItsBackupObjectIsDeleted(t *testing.T) {
	fx, name := startedRestoreOfADeletedVM(t, "gonevmb")
	vmb := &v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: fx.vmb, Namespace: projectNamespace}}
	if err := k8sClient.Delete(context.Background(), vmb); err != nil {
		t.Fatal(err)
	}
	requireFailedAndReleased(t, fx, name)
}

func TestRestoreOfADeletedVMFailsWhenTheBackupLeavesTheList(t *testing.T) {
	fx, name := startedRestoreOfADeletedVM(t, "goneid")
	vmb := &v1alpha1.VirtualMachineBackup{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: projectNamespace, Name: fx.vmb}, vmb); err != nil {
		t.Fatal(err)
	}
	vmb.Status.Backups = nil
	if err := k8sClient.Status().Update(context.Background(), vmb); err != nil {
		t.Fatal(err)
	}
	requireFailedAndReleased(t, fx, name)
}

func TestRestoreOfADeletedVMFailsWhenTheVeleroBackupIsGone(t *testing.T) {
	fx, name := startedRestoreOfADeletedVM(t, "gonevelero")
	backup, err := getVelero(t, "Backup", fx.backupID)
	if err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	requireFailedAndReleased(t, fx, name)
}

func TestBlockersAreCappedAtFive(t *testing.T) {
	names := []string{"a/1", "a/2", "a/3", "a/4", "a/5", "a/6", "a/7"}
	if got := blockerList(names[:5]); got != "a/1, a/2, a/3, a/4, a/5" {
		t.Fatalf("five names: %q", got)
	}
	if got := blockerList(names); got != "a/1, a/2, a/3, a/4, a/5 and 2 more" {
		t.Fatalf("seven names: %q", got)
	}
}

func TestPreparingRequeueBacksOff(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want time.Duration
	}{
		{-time.Minute, 10 * time.Second},
		{30 * time.Second, 10 * time.Second},
		{2 * time.Minute, 30 * time.Second},
		{10 * time.Minute, 2 * time.Minute},
		{72 * time.Hour, 2 * time.Minute},
	}
	for _, tc := range cases {
		if got := preparingRequeue(tc.age); got != tc.want {
			t.Errorf("age %v: got %v, want %v", tc.age, got, tc.want)
		}
	}
}

func TestRestoreOfADeletedVMNamesTheObjectsItWaitsFor(t *testing.T) {
	fx := fixtureOf("named")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createKubeVirtObjects(t, fx)
	createVMR(t, "named-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	reconcileVMR(t, newRestoreReconciler(), projectNamespace, "named-restore")
	vmr := getVMR(t, "named-restore")
	requireRestoreState(t, vmr, v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
	message := vmr.Status.Conditions[0].Message
	for _, want := range []string{"VirtualMachine/" + fx.vm, "DataVolume/" + fx.vm + "-root", "PersistentVolumeClaim/" + fx.vm + "-root"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q lacks %q", message, want)
		}
	}
}

func TestRestoreOfADeletedVMKeepsWaitingAndSlowsDown(t *testing.T) {
	fx := fixtureOf("patient")
	prepareDeletedVM(t, fx, snapshotFor(fx))
	createKubeVirtObjects(t, fx)
	createVMR(t, "patient-restore", projectNamespace, v1alpha1.VirtualMachineRestoreSpec{BackupRef: fx.vmb, BackupID: fx.backupID, TargetName: fx.vm})
	late := &RestoreReconciler{Client: k8sClient, Now: func() time.Time { return time.Now().Add(72 * time.Hour) }}
	result := reconcileVMR(t, late, projectNamespace, "patient-restore")
	if result.RequeueAfter != 2*time.Minute {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	requireRestoreState(t, getVMR(t, "patient-restore"), v1alpha1.RestorePhaseInProgress, metav1.ConditionFalse, RestoreReasonPreparing)
	if hold := holdOf(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: fx.vm, Namespace: projectNamespace}}); hold != "patient-restore" {
		t.Fatalf("the hold must stay: %q", hold)
	}
}
