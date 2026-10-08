package backup

import (
	"context"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	ReasonScheduled              = "Scheduled"
	ReasonCompleted              = "Completed"
	ReasonTargetNotConfigured    = "TargetNotConfigured"
	ReasonTargetNotFound         = "TargetNotFound"
	ReasonTargetUnavailable      = "TargetUnavailable"
	ReasonVirtualMachineNotFound = "VirtualMachineNotFound"
	ReasonScheduleInvalid        = "ScheduleInvalid"
	ReasonScheduleTooFrequent    = "ScheduleTooFrequent"
	ReasonKeepInvalid            = "KeepInvalid"
	ReasonNamespaceNotProject    = "NamespaceNotProject"
	ReasonVeleroNotInstalled     = "VeleroNotInstalled"
	ReasonInProgress             = "InProgress"
	ReasonBackupFailed           = "BackupFailed"
	ReasonDeleting               = "Deleting"

	requeueBusy      = 15 * time.Second
	requeueDeleting  = 10 * time.Second
	deletionTimeout  = 10 * time.Minute
	ReasonLeftBehind = "BackupsLeftBehind"
)

var (
	backupListGVK        = schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: "BackupList"}
	dataUploadListGVK    = schema.GroupVersionKind{Group: "velero.io", Version: "v2alpha1", Kind: "DataUploadList"}
	locationGVK          = schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: "BackupStorageLocation"}
	deleteRequestListGVK = schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: "DeleteBackupRequestList"}
)

type BackupReconciler struct {
	Client   client.Client
	Now      func() time.Time
	Recorder record.EventRecorder
}

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	vmb := &v1alpha1.VirtualMachineBackup{}
	if err := r.Client.Get(ctx, req.NamespacedName, vmb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !vmb.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, vmb)
	}
	if err := r.ensureFinalizer(ctx, vmb); err != nil {
		return ctrl.Result{}, err
	}
	scope, refusal, err := r.scopeOf(ctx, vmb)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refusal != nil {
		return r.refuse(ctx, vmb, *refusal, 0)
	}
	desired, refusal := desiredFor(*vmb, scope, r.Now())
	if refusal != nil {
		return r.refuse(ctx, vmb, *refusal, 0)
	}
	if refusal, err = r.locationReady(ctx); err != nil {
		return ctrl.Result{}, err
	}
	if refusal != nil {
		return r.refuse(ctx, vmb, *refusal, requeueInstalling)
	}
	vm, refusal, err := r.virtualMachine(ctx, vmb)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refusal != nil {
		return r.refuse(ctx, vmb, *refusal, requeueInstalling)
	}
	if err := r.markBackupTargets(ctx, vm); err != nil {
		return ctrl.Result{}, err
	}
	return r.drive(ctx, vmb, scope, desired)
}

type refusal struct {
	Reason  string
	Message string
}

func refusalOf(err error) refusal {
	reasons := []struct {
		target error
		reason string
	}{
		{ErrTargetNotFound, ReasonTargetNotFound},
		{ErrKeepInvalid, ReasonKeepInvalid},
		{ErrScheduleTooFrequent, ReasonScheduleTooFrequent},
		{ErrScheduleInvalid, ReasonScheduleInvalid},
	}
	for _, candidate := range reasons {
		if errors.Is(err, candidate.target) {
			return refusal{Reason: candidate.reason, Message: err.Error()}
		}
	}
	return refusal{Reason: ReasonScheduleInvalid, Message: err.Error()}
}

func desiredFor(vmb v1alpha1.VirtualMachineBackup, scope Scope, now time.Time) (*unstructured.Unstructured, *refusal) {
	build := func() (*unstructured.Unstructured, error) { return OneShotFor(vmb, scope) }
	if vmb.Spec.Schedule != "" {
		build = func() (*unstructured.Unstructured, error) { return ScheduleFor(vmb, scope, now) }
	}
	desired, err := build()
	if err != nil {
		why := refusalOf(err)
		return nil, &why
	}
	return desired, nil
}

func (r *BackupReconciler) ensureFinalizer(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup) error {
	if controllerutil.ContainsFinalizer(vmb, BackupFinalizer) {
		return nil
	}
	before := vmb.DeepCopy()
	controllerutil.AddFinalizer(vmb, BackupFinalizer)
	return r.Client.Patch(ctx, vmb, client.MergeFrom(before), client.FieldOwner(v1alpha1.BackupFieldManager))
}

func (r *BackupReconciler) scopeOf(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup) (Scope, *refusal, error) {
	ns := &corev1.Namespace{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: vmb.Namespace}, ns); err != nil {
		return Scope{}, nil, err
	}
	scope, ok := ScopeOf(*ns)
	if !ok {
		return Scope{}, &refusal{Reason: ReasonNamespaceNotProject, Message: "namespace " + vmb.Namespace + " is not a project namespace"}, nil
	}
	return scope, nil, nil
}

func (r *BackupReconciler) locationReady(ctx context.Context) (*refusal, error) {
	location := &unstructured.Unstructured{}
	location.SetGroupVersionKind(locationGVK)
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: VeleroNamespace, Name: LocationName}, location)
	switch {
	case meta.IsNoMatchError(err):
		return &refusal{Reason: ReasonVeleroNotInstalled, Message: "Velero is not installed"}, nil
	case apierrors.IsNotFound(err):
		return &refusal{Reason: ReasonTargetNotConfigured, Message: "backup.target is not configured"}, nil
	case err != nil:
		return nil, err
	}
	if phase, _, _ := unstructured.NestedString(location.Object, "status", "phase"); phase != "Available" {
		return &refusal{Reason: ReasonTargetUnavailable, Message: "the backup location is not available"}, nil
	}
	return nil, nil
}

func (r *BackupReconciler) virtualMachine(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup) (*v1alpha1.VirtualMachine, *refusal, error) {
	vm := &v1alpha1.VirtualMachine{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: vmb.Namespace, Name: vmb.Spec.VMRef}, vm)
	if apierrors.IsNotFound(err) {
		return nil, &refusal{Reason: ReasonVirtualMachineNotFound, Message: "virtual machine " + vmb.Spec.VMRef + " does not exist"}, nil
	}
	return vm, nil, err
}

func (r *BackupReconciler) markBackupTargets(ctx context.Context, vm *v1alpha1.VirtualMachine) error {
	if err := r.labelBackupVM(ctx, vm, vm.Name); err != nil {
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
		if err := r.labelBackupVM(ctx, volume, vm.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r *BackupReconciler) labelBackupVM(ctx context.Context, obj client.Object, vm string) error {
	if obj.GetLabels()[BackupVMLabel] == vm {
		return nil
	}
	patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"`+BackupVMLabel+`":"`+vm+`"}}}`))
	return r.Client.Patch(ctx, obj, patch, client.FieldOwner(v1alpha1.BackupFieldManager))
}

func (r *BackupReconciler) drive(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup, scope Scope, desired *unstructured.Unstructured) (ctrl.Result, error) {
	labels := v1alpha1.TenantLabels("VirtualMachineBackup", vmb.Name, scope.Tenant, scope.Project)
	backups, err := r.ensureVelero(ctx, vmb, desired, labels)
	if err != nil {
		return r.veleroFailure(ctx, vmb, err)
	}
	infos, err := r.infosOf(ctx, backups)
	if err != nil {
		return ctrl.Result{}, err
	}
	if desired.GetKind() == "Schedule" {
		infos, err = r.prune(ctx, vmb, scope, infos)
		if err != nil {
			return r.veleroFailure(ctx, vmb, err)
		}
	}
	summary := Summarize(infos)
	phase := summary.Phase
	if len(infos) == 0 && !isFresh(vmb) {
		phase = vmb.Status.Phase
	}
	r.writeStatus(ctx, vmb, observedStatus(*vmb, summary, phase, newestRaw(infos), vmb.Spec.Schedule != ""))
	return ctrl.Result{RequeueAfter: requeueFor(phase)}, nil
}

func isFresh(vmb *v1alpha1.VirtualMachineBackup) bool {
	return vmb.Status.Phase == "" || vmb.Status.Phase == v1alpha1.BackupPhasePending
}

func (r *BackupReconciler) ensureVelero(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup, desired *unstructured.Unstructured, labels map[string]string) ([]unstructured.Unstructured, error) {
	if desired.GetKind() == "Schedule" {
		if err := ssa.Apply(ctx, r.Client, desired, v1alpha1.BackupFieldManager); err != nil {
			return nil, err
		}
		return r.listVelero(ctx, backupListGVK, labels)
	}
	backups, err := r.listVelero(ctx, backupListGVK, labels)
	if err != nil || len(backups) > 0 || !isFresh(vmb) {
		return backups, err
	}
	if err := client.IgnoreAlreadyExists(r.Client.Create(ctx, desired, client.FieldOwner(v1alpha1.BackupFieldManager))); err != nil {
		return nil, err
	}
	return r.listVelero(ctx, backupListGVK, labels)
}

func (r *BackupReconciler) veleroFailure(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup, err error) (ctrl.Result, error) {
	if meta.IsNoMatchError(err) {
		return r.refuse(ctx, vmb, refusal{Reason: ReasonVeleroNotInstalled, Message: "Velero is not installed"}, requeueInstalling)
	}
	return ctrl.Result{}, err
}

func (r *BackupReconciler) listVelero(ctx context.Context, gvk schema.GroupVersionKind, labels map[string]string) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.Client.List(ctx, list, client.InNamespace(VeleroNamespace), client.MatchingLabels(labels)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *BackupReconciler) infosOf(ctx context.Context, backups []unstructured.Unstructured) ([]BackupInfo, error) {
	infos := make([]BackupInfo, 0, len(backups))
	for i := range backups {
		size, err := r.uploadedBytes(ctx, backups[i].GetName())
		if err != nil {
			return nil, err
		}
		infos = append(infos, InfoOf(&backups[i], size))
	}
	return infos, nil
}

func (r *BackupReconciler) uploadedBytes(ctx context.Context, backupName string) (int64, error) {
	uploads, err := r.listVelero(ctx, dataUploadListGVK, map[string]string{"velero.io/backup-name": backupName})
	if meta.IsNoMatchError(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var total int64
	for _, upload := range uploads {
		total += totalBytesOf(upload)
	}
	return total, nil
}

func totalBytesOf(upload unstructured.Unstructured) int64 {
	value, found, _ := unstructured.NestedFieldNoCopy(upload.Object, "status", "progress", "totalBytes")
	if !found {
		return 0
	}
	switch n := value.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func newestRaw(infos []BackupInfo) string {
	if len(infos) == 0 {
		return ""
	}
	newest := infos[0]
	for _, info := range infos {
		if compareNewestFirst(info, newest) < 0 {
			newest = info
		}
	}
	return newest.Phase
}

func requeueFor(phase string) time.Duration {
	if phase == v1alpha1.BackupPhaseInProgress || phase == v1alpha1.BackupPhasePending {
		return requeueBusy
	}
	return requeueIdle
}

func readyCondition(phase string, scheduled bool, rawPhase string) metav1.Condition {
	switch {
	case phase == v1alpha1.BackupPhaseFailed:
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonBackupFailed, Message: "Velero backup ended in phase " + rawPhase}
	case phase == v1alpha1.BackupPhaseInProgress:
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonInProgress, Message: "a backup is running"}
	case scheduled:
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonScheduled, Message: "the schedule is applied"}
	case phase == v1alpha1.BackupPhaseCompleted:
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonCompleted, Message: "the backup is complete"}
	default:
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonInProgress, Message: "the backup is starting"}
	}
}

func recordsOf(infos []BackupInfo) []v1alpha1.VMBackupRecord {
	records := make([]v1alpha1.VMBackupRecord, 0, len(infos))
	for _, info := range infos {
		records = append(records, v1alpha1.VMBackupRecord{ID: info.ID, Time: metav1.NewTime(info.Time), SizeBytes: info.Bytes})
	}
	return records
}

func observedStatus(vmb v1alpha1.VirtualMachineBackup, summary Summary, phase, rawPhase string, scheduled bool) v1alpha1.VirtualMachineBackupStatus {
	status := *vmb.Status.DeepCopy()
	status.ObservedGeneration = vmb.Generation
	status.Phase = phase
	status.Backups = recordsOf(summary.Completed)
	status.LastBackup = nil
	if !summary.Last.IsZero() {
		last := metav1.NewTime(summary.Last)
		status.LastBackup = &last
	}
	v1alpha1.SetCondition(&status.Conditions, readyCondition(phase, scheduled, rawPhase))
	return status
}

func refusedStatus(vmb v1alpha1.VirtualMachineBackup, why refusal) v1alpha1.VirtualMachineBackupStatus {
	status := *vmb.Status.DeepCopy()
	status.ObservedGeneration = vmb.Generation
	v1alpha1.SetCondition(&status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: why.Reason, Message: why.Message})
	return status
}

func (r *BackupReconciler) refuse(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup, why refusal, requeue time.Duration) (ctrl.Result, error) {
	r.writeStatus(ctx, vmb, refusedStatus(*vmb, why))
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *BackupReconciler) writeStatus(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup, desired v1alpha1.VirtualMachineBackupStatus) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1alpha1.VirtualMachineBackup{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(vmb), current); err != nil {
			return err
		}
		current.Status = desired
		return r.Client.Status().Update(ctx, current, client.FieldOwner(v1alpha1.BackupFieldManager))
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "write backup status")
	}
}

func (r *BackupReconciler) prune(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup, scope Scope, infos []BackupInfo) ([]BackupInfo, error) {
	deleting, err := r.pendingDeletions(ctx, scope)
	if err != nil {
		return nil, err
	}
	live := withoutIDs(infos, deleting)
	doomed := Prune(live, KeepOf(*vmb))
	for _, id := range doomed {
		if err := r.requestDeletion(ctx, scope, id); err != nil {
			return nil, err
		}
	}
	return withoutIDs(live, doomed), nil
}

func (r *BackupReconciler) pendingDeletions(ctx context.Context, scope Scope) ([]string, error) {
	labels := map[string]string{v1alpha1.LabelKind: "DeleteBackupRequest", v1alpha1.LabelTenant: scope.Tenant, v1alpha1.LabelProject: scope.Project}
	requests, err := r.listVelero(ctx, deleteRequestListGVK, labels)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(requests))
	for _, request := range requests {
		name, _, _ := unstructured.NestedString(request.Object, "spec", "backupName")
		names = append(names, name)
	}
	return names, nil
}

func (r *BackupReconciler) requestDeletion(ctx context.Context, scope Scope, backupName string) error {
	return client.IgnoreAlreadyExists(r.Client.Create(ctx, DeleteBackupRequest(scope, backupName), client.FieldOwner(v1alpha1.BackupFieldManager)))
}

func withoutIDs(infos []BackupInfo, ids []string) []BackupInfo {
	kept := make([]BackupInfo, 0, len(infos))
	for _, info := range infos {
		if !slices.Contains(ids, info.ID) {
			kept = append(kept, info)
		}
	}
	return kept
}

func (r *BackupReconciler) finalize(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(vmb, BackupFinalizer) {
		return ctrl.Result{}, nil
	}
	remaining, err := r.removeVeleroObjects(ctx, vmb)
	if meta.IsNoMatchError(err) {
		return ctrl.Result{}, r.releaseFinalizer(ctx, vmb)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if remaining == 0 {
		return ctrl.Result{}, r.releaseFinalizer(ctx, vmb)
	}
	if r.Now().Sub(vmb.DeletionTimestamp.Time) > deletionTimeout {
		r.Recorder.Eventf(vmb, corev1.EventTypeWarning, ReasonLeftBehind, "%d Velero backups remain after the deletion timeout", remaining)
		return ctrl.Result{}, r.releaseFinalizer(ctx, vmb)
	}
	r.writeStatus(ctx, vmb, refusedStatus(*vmb, refusal{Reason: ReasonDeleting, Message: "the Velero backups are being deleted"}))
	return ctrl.Result{RequeueAfter: requeueDeleting}, nil
}

func (r *BackupReconciler) removeVeleroObjects(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup) (int, error) {
	scope, refused, err := r.scopeOf(ctx, vmb)
	if err != nil || refused != nil {
		return 0, err
	}
	schedule := veleroStub("Schedule", VeleroName("vmb", scope.Namespace, vmb.Name))
	if err := client.IgnoreNotFound(r.Client.Delete(ctx, schedule)); err != nil {
		return 0, err
	}
	backups, err := r.listVelero(ctx, backupListGVK, v1alpha1.TenantLabels("VirtualMachineBackup", vmb.Name, scope.Tenant, scope.Project))
	if err != nil {
		return 0, err
	}
	for _, backup := range backups {
		if err := r.requestDeletion(ctx, scope, backup.GetName()); err != nil {
			return 0, err
		}
	}
	return len(backups), nil
}

func (r *BackupReconciler) releaseFinalizer(ctx context.Context, vmb *v1alpha1.VirtualMachineBackup) error {
	before := vmb.DeepCopy()
	controllerutil.RemoveFinalizer(vmb, BackupFinalizer)
	return client.IgnoreNotFound(r.Client.Patch(ctx, vmb, client.MergeFrom(before), client.FieldOwner(v1alpha1.BackupFieldManager)))
}

func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("backup-vmb").For(&v1alpha1.VirtualMachineBackup{}).Complete(r)
}
