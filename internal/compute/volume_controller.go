package compute

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const FinalizerVolumeInUse = "bedrock.cloudyfolks.io/volume-in-use"

type VolumeReconciler struct {
	Client client.Client
}

type volumeOutcome struct {
	Gate       *Gate
	Phase      string
	AttachedTo string
	ActualGi   int32
}

func (r *VolumeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var volume v1alpha1.Volume
	if err := r.Client.Get(ctx, req.NamespacedName, &volume); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var vms v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &vms, client.InNamespace(volume.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	owner := VolumeOwner(vms.Items, volume.Name)
	if !volume.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.release(ctx, volume, owner)
	}
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: volume.Namespace}, &ns); err != nil {
		return ctrl.Result{}, err
	}
	labels, inProject := OwnerLabels("Volume", volume.Name, ns)
	if !inProject {
		return ctrl.Result{}, r.writeStatus(ctx, volume, volumeOutcome{Gate: gateOf(ReasonNotInProject, "the namespace is not a project namespace")})
	}
	if holder, held := v1alpha1.RestoreHolder(&volume); held {
		return ctrl.Result{}, r.writeStatus(ctx, volume, volumeOutcome{Gate: gateOf(ReasonRestoreInProgress, "the restore %q holds the volume", holder)})
	}
	volume, err := r.addFinalizer(ctx, volume)
	if err != nil {
		return ctrl.Result{}, err
	}
	done, err := r.converge(ctx, volume, labels, owner)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.writeStatus(ctx, volume, done)
}

func (r *VolumeReconciler) addFinalizer(ctx context.Context, volume v1alpha1.Volume) (v1alpha1.Volume, error) {
	if controllerutil.ContainsFinalizer(&volume, FinalizerVolumeInUse) {
		return volume, nil
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), &volume); err != nil {
			return err
		}
		if !controllerutil.AddFinalizer(&volume, FinalizerVolumeInUse) {
			return nil
		}
		return r.Client.Update(ctx, &volume, client.FieldOwner(v1alpha1.ComputeFieldManager))
	})
	return volume, err
}

func (r *VolumeReconciler) removeFinalizer(ctx context.Context, volume v1alpha1.Volume) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), &volume); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !controllerutil.RemoveFinalizer(&volume, FinalizerVolumeInUse) {
			return nil
		}
		return client.IgnoreNotFound(r.Client.Update(ctx, &volume, client.FieldOwner(v1alpha1.ComputeFieldManager)))
	})
}

func (r *VolumeReconciler) release(ctx context.Context, volume v1alpha1.Volume, owner string) error {
	if !controllerutil.ContainsFinalizer(&volume, FinalizerVolumeInUse) {
		return nil
	}
	if owner != "" {
		return r.writeStatus(ctx, volume, volumeOutcome{Gate: gateOf(ReasonInUse, "the virtual machine %q uses the volume", owner), AttachedTo: owner, Phase: v1alpha1.VolumePhaseInUse})
	}
	return r.removeFinalizer(ctx, volume)
}

func (r *VolumeReconciler) converge(ctx context.Context, volume v1alpha1.Volume, labels map[string]string, owner string) (volumeOutcome, error) {
	source, gate, err := r.resolveSource(ctx, volume)
	if err != nil || gate != nil {
		return volumeOutcome{Gate: gate, AttachedTo: owner}, err
	}
	claim, err := VolumeClaim(volume, labels, source)
	if err != nil {
		return volumeOutcome{Gate: gateOf(ReasonSourceNotReady, "%s", err.Error()), AttachedTo: owner}, nil
	}
	if err := createIfAbsent(ctx, r.Client, claim); err != nil {
		var conflict nameConflict
		if errors.As(err, &conflict) {
			return volumeOutcome{Gate: gateOf(ReasonNameConflict, "%s", conflict.Error()), AttachedTo: owner}, nil
		}
		return volumeOutcome{}, fmt.Errorf("create the claim: %w", err)
	}
	var pvc corev1.PersistentVolumeClaim
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), &pvc)
	if apierrors.IsNotFound(err) {
		return r.observe(ctx, volume, owner, nil)
	}
	if err != nil {
		return volumeOutcome{}, err
	}
	if gate, err := r.resize(ctx, volume, pvc); err != nil || gate != nil {
		return volumeOutcome{Gate: gate, AttachedTo: owner}, err
	}
	if err := labelClaim(ctx, r.Client, volume.Namespace, volume.Name, labels); err != nil {
		return volumeOutcome{}, fmt.Errorf("label the claim: %w", err)
	}
	return r.observe(ctx, volume, owner, &pvc)
}

func (r *VolumeReconciler) resolveSource(ctx context.Context, volume v1alpha1.Volume) (ClaimSource, *Gate, error) {
	source := volume.Spec.Source
	if source == nil {
		return ClaimSource{}, nil, nil
	}
	switch {
	case source.Image != nil:
		return r.imageSource(ctx, volume, *source.Image)
	case source.SnapshotRef != "":
		return r.snapshotSource(ctx, volume, source.SnapshotRef)
	case source.VolumeRef != "":
		return r.cloneSource(ctx, volume, source.VolumeRef)
	}
	return ClaimSource{}, nil, nil
}

func (r *VolumeReconciler) imageSource(ctx context.Context, volume v1alpha1.Volume, ref v1alpha1.ImageRef) (ClaimSource, *Gate, error) {
	image, _, gate, err := lookupImage(ctx, r.Client, volume.Namespace, ref, v1alpha1.ImageTypeDisk)
	if err != nil || gate != nil {
		return ClaimSource{}, sourceGate(gate), err
	}
	if volume.Spec.SizeGi < image.SizeGi {
		return ClaimSource{}, gateOf(ReasonSourceNotReady, "the volume of %dGi is smaller than the image of %dGi", volume.Spec.SizeGi, image.SizeGi), nil
	}
	return ClaimSource{Image: &image}, nil, nil
}

func sourceGate(gate *Gate) *Gate {
	if gate == nil {
		return nil
	}
	return &Gate{Reason: ReasonSourceNotReady, Message: gate.Message}
}

func (r *VolumeReconciler) snapshotSource(ctx context.Context, volume v1alpha1.Volume, name string) (ClaimSource, *Gate, error) {
	var snapshot v1alpha1.VolumeSnapshot
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: volume.Namespace, Name: name}, &snapshot)
	if apierrors.IsNotFound(err) || (err == nil && snapshot.Status.Phase != v1alpha1.VolumeSnapshotPhaseReady) {
		return ClaimSource{}, gateOf(ReasonSourceNotReady, "the snapshot %q is not ready", name), nil
	}
	if err != nil {
		return ClaimSource{}, nil, err
	}
	if sizeGi := ceilGi(snapshot.Status.SizeBytes); volume.Spec.SizeGi < sizeGi {
		return ClaimSource{}, gateOf(ReasonSourceNotReady, "the volume of %dGi is smaller than the snapshot of %dGi", volume.Spec.SizeGi, sizeGi), nil
	}
	return ClaimSource{SnapshotName: name}, nil, nil
}

func ceilGi(bytes int64) int32 {
	return int32((bytes + bytesPerGi - 1) / bytesPerGi)
}

func (r *VolumeReconciler) cloneSource(ctx context.Context, volume v1alpha1.Volume, name string) (ClaimSource, *Gate, error) {
	var pvc corev1.PersistentVolumeClaim
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: volume.Namespace, Name: name}, &pvc)
	if apierrors.IsNotFound(err) {
		return ClaimSource{}, gateOf(ReasonSourceNotReady, "the volume %q does not exist", name), nil
	}
	if err != nil {
		return ClaimSource{}, nil, err
	}
	if sizeGi := ceilGi(pvc.Spec.Resources.Requests.Storage().Value()); volume.Spec.SizeGi < sizeGi {
		return ClaimSource{}, gateOf(ReasonSourceNotReady, "the volume of %dGi is smaller than the source of %dGi", volume.Spec.SizeGi, sizeGi), nil
	}
	return ClaimSource{VolumeName: name}, nil, nil
}

func (r *VolumeReconciler) resize(ctx context.Context, volume v1alpha1.Volume, pvc corev1.PersistentVolumeClaim) (*Gate, error) {
	requested := ceilGi(pvc.Spec.Resources.Requests.Storage().Value())
	switch {
	case volume.Spec.SizeGi < requested:
		return gateOf(ReasonShrinkRefused, "the claim holds %dGi and cannot shrink to %dGi", requested, volume.Spec.SizeGi), nil
	case volume.Spec.SizeGi == requested:
		return nil, nil
	}
	expandable, err := r.expandable(ctx, pvc)
	if err != nil {
		return nil, err
	}
	if !expandable {
		return gateOf(ReasonExpansionUnsupported, "the storage class of the volume does not allow expansion"), nil
	}
	return nil, growClaim(ctx, r.Client, volume.Namespace, volume.Name, volume.Spec.SizeGi)
}

func (r *VolumeReconciler) expandable(ctx context.Context, pvc corev1.PersistentVolumeClaim) (bool, error) {
	if pvc.Spec.StorageClassName == nil {
		return false, nil
	}
	var class storagev1.StorageClass
	err := r.Client.Get(ctx, client.ObjectKey{Name: *pvc.Spec.StorageClassName}, &class)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil && class.AllowVolumeExpansion != nil && *class.AllowVolumeExpansion, err
}

func (r *VolumeReconciler) observe(ctx context.Context, volume v1alpha1.Volume, owner string, pvc *corev1.PersistentVolumeClaim) (volumeOutcome, error) {
	claim := emptyOf(dataVolumeGVK)
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), claim)
	if err != nil && !notFound(err) {
		return volumeOutcome{}, err
	}
	phase, _, _ := unstructured.NestedString(claim.Object, "status", "phase")
	var capacity int32
	if pvc != nil {
		capacity = ActualSizeGi(pvc.Status.Capacity[corev1.ResourceStorage])
	}
	return volumeOutcome{Phase: VolumePhaseOf(phase, owner, capacity, volume.Spec.SizeGi), AttachedTo: owner, ActualGi: capacity}, nil
}

func volumeCondition(volume v1alpha1.Volume, done volumeOutcome) metav1.Condition {
	cond := metav1.Condition{Type: v1alpha1.ConditionReady, ObservedGeneration: volume.Generation}
	switch {
	case done.Gate != nil:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, done.Gate.Reason, done.Gate.Message
	case done.Phase == v1alpha1.VolumePhaseAvailable || done.Phase == v1alpha1.VolumePhaseInUse:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "Bound", "the volume is ready"
	default:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, done.Phase, "the volume is "+done.Phase
	}
	return cond
}

func (r *VolumeReconciler) writeStatus(ctx context.Context, volume v1alpha1.Volume, done volumeOutcome) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Volume
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		desired.Status.ObservedGeneration = current.Generation
		desired.Status.AttachedTo, desired.Status.ActualSizeGi = done.AttachedTo, done.ActualGi
		done.Phase = phaseOf(current, done)
		desired.Status.Phase = done.Phase
		desired.Status.Conditions = mergeConditions(current.Status.Conditions, []metav1.Condition{volumeCondition(current, done)}, metav1.Now().Time)
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.ComputeFieldManager))
	})
}

func phaseOf(current v1alpha1.Volume, done volumeOutcome) string {
	switch {
	case done.Phase != "":
		return done.Phase
	case current.Status.Phase != "":
		return current.Status.Phase
	default:
		return v1alpha1.VolumePhasePending
	}
}

func (r *VolumeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("compute-volume").
		For(&v1alpha1.Volume{}).
		Owns(emptyOf(dataVolumeGVK)).
		Watches(&corev1.PersistentVolumeClaim{}, handler.EnqueueRequestsFromMapFunc(sameName)).
		Watches(&v1alpha1.VirtualMachine{}, handler.EnqueueRequestsFromMapFunc(listedVolumes)).
		Watches(&v1alpha1.VolumeSnapshot{}, handler.EnqueueRequestsFromMapFunc(r.cloningFrom)).
		Complete(r)
}

func listedVolumes(_ context.Context, obj client.Object) []reconcile.Request {
	vm, ok := obj.(*v1alpha1.VirtualMachine)
	if !ok {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(vm.Spec.Disks))
	for _, disk := range vm.Spec.Disks {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Namespace: vm.Namespace, Name: disk.VolumeRef}})
	}
	return requests
}

func (r *VolumeReconciler) cloningFrom(ctx context.Context, obj client.Object) []reconcile.Request {
	var volumes v1alpha1.VolumeList
	if err := r.Client.List(ctx, &volumes, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for index := range volumes.Items {
		source := volumes.Items[index].Spec.Source
		if source != nil && source.SnapshotRef == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&volumes.Items[index])})
		}
	}
	return requests
}
