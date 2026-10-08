package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	RestoreReasonBackupNotFound         = "BackupNotFound"
	RestoreReasonRenameNotSupported     = "RenameNotSupported"
	RestoreReasonTargetExists           = "TargetExists"
	RestoreReasonVirtualMachineNotFound = "VirtualMachineNotFound"
	RestoreReasonPreparing              = "Preparing"
	RestoreReasonRestoring              = "Restoring"
	RestoreReasonVeleroNotInstalled     = "VeleroNotInstalled"
	RestoreReasonFailed                 = "Failed"
	RestoreReasonDeleting               = "Deleting"
	RestoreReasonCompleted              = "Completed"

	requeuePreparing = 10 * time.Second
)

var (
	kubevirtVMListGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineList"}
	dataVolumeListGVK = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolumeList"}
	pvcListGVK        = schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaimList"}
)

type RestoreReconciler struct {
	Client client.Client
	Now    func() time.Time
}

type stop struct {
	Phase   string
	Reason  string
	Message string
	Ready   metav1.ConditionStatus
	Requeue time.Duration
}

func (r *RestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	vmr := &v1alpha1.VirtualMachineRestore{}
	if err := r.Client.Get(ctx, req.NamespacedName, vmr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !vmr.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, vmr)
	}
	if vmr.Status.Phase == v1alpha1.RestorePhaseCompleted || vmr.Status.Phase == v1alpha1.RestorePhaseFailed {
		return ctrl.Result{}, nil
	}
	if err := r.ensureFinalizer(ctx, vmr); err != nil {
		return ctrl.Result{}, err
	}
	next, err := r.advance(ctx, vmr)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.conclude(ctx, vmr, next)
}

func (r *RestoreReconciler) conclude(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore, next stop) (ctrl.Result, error) {
	releases := next.Phase == v1alpha1.RestorePhaseFailed || next.Phase == v1alpha1.RestorePhaseCompleted ||
		(next.Phase == v1alpha1.RestorePhasePending && vmr.Status.Phase == v1alpha1.RestorePhaseInProgress)
	if releases {
		if err := r.releaseHold(ctx, vmr); err != nil {
			return ctrl.Result{}, err
		}
	}
	r.writeStatus(ctx, vmr, next)
	return ctrl.Result{RequeueAfter: next.Requeue}, nil
}

func halt(phase, reason, message string, requeue time.Duration) stop {
	return stop{Phase: phase, Reason: reason, Message: message, Ready: metav1.ConditionFalse, Requeue: requeue}
}

func (r *RestoreReconciler) advance(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore) (stop, error) {
	scope, vmb, next, err := r.resolve(ctx, vmr)
	if err != nil || next != nil {
		return orZero(next), err
	}
	backupID, found := resolveBackupID(*vmb, vmr.Spec.BackupID)
	if !found {
		return halt(v1alpha1.RestorePhasePending, RestoreReasonBackupNotFound, "backup ID is not in the backup list", requeueInstalling), nil
	}
	vm := vmb.Spec.VMRef
	if !vmr.Spec.Replace && vmr.Spec.TargetName != vm {
		return halt(v1alpha1.RestorePhaseFailed, RestoreReasonRenameNotSupported, "restore to a different VM name is not supported; use the name "+vm, 0), nil
	}
	restore, err := r.getRestore(ctx, scope, vmr.Name)
	if meta.IsNoMatchError(err) {
		return halt(v1alpha1.RestorePhasePending, RestoreReasonVeleroNotInstalled, "Velero is not installed", requeueInstalling), nil
	}
	if err != nil {
		return stop{}, err
	}
	if restore != nil {
		return outcomeOf(restore), nil
	}
	return r.start(ctx, vmr, scope, vm, backupID)
}

func orZero(next *stop) stop {
	if next == nil {
		return stop{}
	}
	return *next
}

func (r *RestoreReconciler) resolve(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore) (Scope, *v1alpha1.VirtualMachineBackup, *stop, error) {
	ns := &corev1.Namespace{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: vmr.Namespace}, ns); err != nil {
		return Scope{}, nil, nil, err
	}
	scope, ok := ScopeOf(*ns)
	if !ok {
		next := halt(v1alpha1.RestorePhaseFailed, RestoreReasonFailed, "namespace "+vmr.Namespace+" is not a project namespace", 0)
		return Scope{}, nil, &next, nil
	}
	vmb := &v1alpha1.VirtualMachineBackup{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: vmr.Namespace, Name: vmr.Spec.BackupRef}, vmb)
	if apierrors.IsNotFound(err) {
		next := halt(v1alpha1.RestorePhasePending, RestoreReasonBackupNotFound, "backup "+vmr.Spec.BackupRef+" does not exist in this project", requeueInstalling)
		return Scope{}, nil, &next, nil
	}
	return scope, vmb, nil, err
}

func resolveBackupID(vmb v1alpha1.VirtualMachineBackup, requested string) (string, bool) {
	for i, record := range vmb.Status.Backups {
		if requested == "" && i == 0 || record.ID == requested {
			return record.ID, true
		}
	}
	return "", false
}

func (r *RestoreReconciler) getRestore(ctx context.Context, scope Scope, vmr string) (*unstructured.Unstructured, error) {
	restore := veleroStub("Restore", VeleroName("vmr", scope.Namespace, vmr))
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(restore), restore)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return restore, err
}

func (r *RestoreReconciler) start(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore, scope Scope, vm, backupID string) (stop, error) {
	bedrockVM := &v1alpha1.VirtualMachine{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: scope.Namespace, Name: vm}, bedrockVM)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return stop{}, err
	}
	if vmr.Spec.Replace && !exists {
		return halt(v1alpha1.RestorePhasePending, RestoreReasonVirtualMachineNotFound, "virtual machine "+vm+" does not exist; restore it by name instead", requeueInstalling), nil
	}
	if !vmr.Spec.Replace && exists {
		return halt(v1alpha1.RestorePhasePending, RestoreReasonTargetExists, "virtual machine "+vm+" exists; use replace to overwrite it", requeueInstalling), nil
	}
	if halted, err := r.preconditions(ctx, backupID); err != nil || halted != nil {
		return orZero(halted), err
	}
	if vmr.Spec.Replace {
		gone, err := r.prepare(ctx, vmr, scope, bedrockVM)
		if err != nil || !gone {
			return halt(v1alpha1.RestorePhaseInProgress, RestoreReasonPreparing, "the VM objects are being removed", requeuePreparing), err
		}
	}
	return r.launch(ctx, vmr, scope, backupID)
}

func (r *RestoreReconciler) preconditions(ctx context.Context, backupID string) (*stop, error) {
	why, err := locationState(ctx, r.Client)
	if err != nil {
		return nil, err
	}
	if why != nil {
		next := halt(v1alpha1.RestorePhasePending, why.Reason, why.Message, requeueInstalling)
		return &next, nil
	}
	backup := veleroStub("Backup", backupID)
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(backup), backup)
	if apierrors.IsNotFound(err) {
		next := halt(v1alpha1.RestorePhaseFailed, RestoreReasonBackupNotFound, "Velero backup "+backupID+" does not exist", 0)
		return &next, nil
	}
	if err != nil {
		return nil, err
	}
	if phase, _, _ := unstructured.NestedString(backup.Object, "status", "phase"); phase != phaseCompleted {
		next := halt(v1alpha1.RestorePhaseFailed, RestoreReasonBackupNotFound, "Velero backup "+backupID+" is in phase "+phase+", not Completed", 0)
		return &next, nil
	}
	return nil, nil
}

func (r *RestoreReconciler) launch(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore, scope Scope, backupID string) (stop, error) {
	for _, obj := range []client.Object{RestoreModifiers(scope, vmr.Name), Restore(scope, vmr.Name, backupID)} {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.BackupFieldManager); err != nil {
			if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
				return halt(v1alpha1.RestorePhasePending, RestoreReasonVeleroNotInstalled, "Velero is not installed", requeueInstalling), nil
			}
			return stop{}, err
		}
	}
	return halt(v1alpha1.RestorePhaseInProgress, RestoreReasonRestoring, "Velero is restoring the VM", requeueBusy), nil
}

func (r *RestoreReconciler) prepare(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore, scope Scope, vm *v1alpha1.VirtualMachine) (bool, error) {
	if err := r.holdAll(ctx, vmr.Name, vm); err != nil {
		return false, err
	}
	remaining := 0
	for _, selector := range underlyingSelectors(scope, vm) {
		for _, list := range []schema.GroupVersionKind{kubevirtVMListGVK, dataVolumeListGVK, pvcListGVK} {
			left, err := r.removeAll(ctx, list, scope.Namespace, selector)
			if err != nil {
				return false, err
			}
			remaining += left
		}
	}
	return remaining == 0, nil
}

func underlyingSelectors(scope Scope, vm *v1alpha1.VirtualMachine) []map[string]string {
	selectors := []map[string]string{v1alpha1.TenantLabels("VirtualMachine", vm.Name, scope.Tenant, scope.Project)}
	for _, disk := range vm.Spec.Disks {
		selectors = append(selectors, v1alpha1.TenantLabels("Volume", disk.VolumeRef, scope.Tenant, scope.Project))
	}
	return selectors
}

func (r *RestoreReconciler) removeAll(ctx context.Context, list schema.GroupVersionKind, namespace string, labels map[string]string) (int, error) {
	items := &unstructured.UnstructuredList{}
	items.SetGroupVersionKind(list)
	err := r.Client.List(ctx, items, client.InNamespace(namespace), client.MatchingLabels(labels))
	if meta.IsNoMatchError(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for i := range items.Items {
		if items.Items[i].GetDeletionTimestamp() != nil {
			continue
		}
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, &items.Items[i], client.PropagationPolicy(metav1.DeletePropagationForeground))); err != nil {
			return 0, err
		}
	}
	return len(items.Items), nil
}

func (r *RestoreReconciler) holdAll(ctx context.Context, restore string, vm *v1alpha1.VirtualMachine) error {
	if err := r.patchHold(ctx, vm, WithHold(vm.Annotations, restore)); err != nil {
		return err
	}
	for _, disk := range vm.Spec.Disks {
		volume := &v1alpha1.Volume{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: vm.Namespace, Name: disk.VolumeRef}, volume)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := r.patchHold(ctx, volume, WithHold(volume.Annotations, restore)); err != nil {
			return err
		}
	}
	return nil
}

func (r *RestoreReconciler) patchHold(ctx context.Context, obj client.Object, desired map[string]string) error {
	current := obj.GetAnnotations()
	if desired[HoldAnnotation] == current[HoldAnnotation] {
		return nil
	}
	var value any = desired[HoldAnnotation]
	if _, held := desired[HoldAnnotation]; !held {
		value = nil
	}
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{HoldAnnotation: value}}})
	if err != nil {
		return err
	}
	return r.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, body), client.FieldOwner(v1alpha1.BackupFieldManager))
}

func (r *RestoreReconciler) releaseHold(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore) error {
	vms := &v1alpha1.VirtualMachineList{}
	if err := r.Client.List(ctx, vms, client.InNamespace(vmr.Namespace)); err != nil {
		return err
	}
	for i := range vms.Items {
		if err := r.patchHold(ctx, &vms.Items[i], WithoutHold(vms.Items[i].Annotations, vmr.Name)); err != nil {
			return err
		}
	}
	volumes := &v1alpha1.VolumeList{}
	if err := r.Client.List(ctx, volumes, client.InNamespace(vmr.Namespace)); err != nil {
		return err
	}
	for i := range volumes.Items {
		if err := r.patchHold(ctx, &volumes.Items[i], WithoutHold(volumes.Items[i].Annotations, vmr.Name)); err != nil {
			return err
		}
	}
	return nil
}

func outcomeOf(restore *unstructured.Unstructured) stop {
	phase, _, _ := unstructured.NestedString(restore.Object, "status", "phase")
	switch phase {
	case "Completed":
		return stop{Phase: v1alpha1.RestorePhaseCompleted, Reason: RestoreReasonCompleted, Message: "the restore is complete", Ready: metav1.ConditionTrue}
	case "Failed", "PartiallyFailed", "FailedValidation":
		return halt(v1alpha1.RestorePhaseFailed, RestoreReasonFailed, failureMessage(restore, phase), 0)
	default:
		return halt(v1alpha1.RestorePhaseInProgress, RestoreReasonRestoring, "Velero is restoring the VM", requeueBusy)
	}
}

func failureMessage(restore *unstructured.Unstructured, phase string) string {
	reason, _, _ := unstructured.NestedString(restore.Object, "status", "failureReason")
	validation, _, _ := unstructured.NestedSlice(restore.Object, "status", "validationErrors")
	return fmt.Sprintf("Velero restore ended in phase %s: %s; %d validation errors", phase, reason, len(validation))
}

func (r *RestoreReconciler) ensureFinalizer(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore) error {
	if controllerutil.ContainsFinalizer(vmr, RestoreFinalizer) {
		return nil
	}
	before := vmr.DeepCopy()
	controllerutil.AddFinalizer(vmr, RestoreFinalizer)
	return r.Client.Patch(ctx, vmr, client.MergeFrom(before), client.FieldOwner(v1alpha1.BackupFieldManager))
}

func (r *RestoreReconciler) finalize(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(vmr, RestoreFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.releaseHold(ctx, vmr); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deleteVeleroObjects(ctx, vmr); err != nil {
		return ctrl.Result{}, err
	}
	before := vmr.DeepCopy()
	controllerutil.RemoveFinalizer(vmr, RestoreFinalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Client.Patch(ctx, vmr, client.MergeFrom(before), client.FieldOwner(v1alpha1.BackupFieldManager)))
}

func (r *RestoreReconciler) deleteVeleroObjects(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore) error {
	scope := Scope{Namespace: vmr.Namespace}
	modifiers := RestoreModifiers(scope, vmr.Name)
	for _, obj := range []client.Object{veleroStub("Restore", VeleroName("vmr", vmr.Namespace, vmr.Name)), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: modifiers.Name, Namespace: VeleroNamespace}}} {
		err := r.Client.Delete(ctx, obj)
		if err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
	}
	return nil
}

func statusAfter(vmr v1alpha1.VirtualMachineRestore, next stop) v1alpha1.VirtualMachineRestoreStatus {
	status := *vmr.Status.DeepCopy()
	status.ObservedGeneration = vmr.Generation
	status.Phase = next.Phase
	v1alpha1.SetCondition(&status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: next.Ready, Reason: next.Reason, Message: next.Message})
	return status
}

func (r *RestoreReconciler) writeStatus(ctx context.Context, vmr *v1alpha1.VirtualMachineRestore, next stop) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1alpha1.VirtualMachineRestore{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(vmr), current); err != nil {
			return err
		}
		current.Status = statusAfter(*current, next)
		return r.Client.Status().Update(ctx, current, client.FieldOwner(v1alpha1.BackupFieldManager))
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "write restore status")
	}
}

func (r *RestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("backup-vmr").For(&v1alpha1.VirtualMachineRestore{}).Complete(r)
}
