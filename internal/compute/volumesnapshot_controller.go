package compute

import (
	"context"
	"errors"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const annotationDefaultSnapshotClass = "snapshot.storage.kubernetes.io/is-default-class"

type VolumeSnapshotReconciler struct {
	Client client.Client
	Now    func() time.Time
}

type snapshotOutcome struct {
	Gate      *Gate
	Phase     string
	SizeBytes int64
	Message   string
	Requeue   time.Duration
}

func SnapshotExpiry(created time.Time, retention *metav1.Duration) (time.Time, bool) {
	if retention == nil || retention.Duration <= 0 {
		return time.Time{}, false
	}
	return created.Add(retention.Duration), true
}

func SnapshotClassFor(provisioner string, classes []unstructured.Unstructured) (string, error) {
	var matching []unstructured.Unstructured
	for _, class := range classes {
		if driver, _, _ := unstructured.NestedString(class.Object, "driver"); driver == provisioner {
			matching = append(matching, class)
		}
	}
	if len(matching) == 0 {
		return "", errors.New("no snapshot class serves the provisioner of the volume")
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].GetName() < matching[j].GetName() })
	for _, class := range matching {
		if class.GetAnnotations()[annotationDefaultSnapshotClass] == "true" {
			return class.GetName(), nil
		}
	}
	return matching[0].GetName(), nil
}

func (r *VolumeSnapshotReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *VolumeSnapshotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var snapshot v1alpha1.VolumeSnapshot
	if err := r.Client.Get(ctx, req.NamespacedName, &snapshot); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !snapshot.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	expiry, expires := SnapshotExpiry(snapshot.CreationTimestamp.Time, snapshot.Spec.Retention)
	if expires && !r.now().Before(expiry) {
		return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, &snapshot))
	}
	done, err := r.converge(ctx, snapshot)
	if err != nil {
		return ctrl.Result{}, err
	}
	if expires {
		done.Requeue = expiry.Sub(r.now())
	}
	return ctrl.Result{RequeueAfter: done.Requeue}, r.writeStatus(ctx, snapshot, done)
}

func (r *VolumeSnapshotReconciler) converge(ctx context.Context, snapshot v1alpha1.VolumeSnapshot) (snapshotOutcome, error) {
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: snapshot.Namespace}, &ns); err != nil {
		return snapshotOutcome{}, err
	}
	labels, ok := OwnerLabels("VolumeSnapshot", snapshot.Name, ns)
	if !ok {
		return snapshotOutcome{Gate: gateOf(ReasonNotInProject, "the namespace is not a project namespace")}, nil
	}
	provisioner, gate, err := r.provisionerOf(ctx, snapshot)
	if err != nil || gate != nil {
		return snapshotOutcome{Gate: gate}, err
	}
	var classes unstructured.UnstructuredList
	classes.SetGroupVersionKind(csiSnapshotClassGVK.GroupVersion().WithKind("VolumeSnapshotClassList"))
	if err := r.Client.List(ctx, &classes); err != nil && !notFound(err) {
		return snapshotOutcome{}, err
	}
	class, err := SnapshotClassFor(provisioner, classes.Items)
	if err != nil {
		return snapshotOutcome{Gate: gateOf(ReasonNoSnapshotClass, "%s", err.Error())}, nil
	}
	claim := csiSnapshot(snapshot, labels, class)
	if err := createIfAbsent(ctx, r.Client, claim); err != nil {
		var conflict nameConflict
		if errors.As(err, &conflict) {
			return snapshotOutcome{Gate: gateOf(ReasonNameConflict, "%s", conflict.Error())}, nil
		}
		return snapshotOutcome{}, err
	}
	return r.observe(ctx, snapshot)
}

func (r *VolumeSnapshotReconciler) provisionerOf(ctx context.Context, snapshot v1alpha1.VolumeSnapshot) (string, *Gate, error) {
	var volume v1alpha1.Volume
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: snapshot.Namespace, Name: snapshot.Spec.VolumeRef}, &volume)
	if apierrors.IsNotFound(err) {
		return "", gateOf(ReasonVolumeNotFound, "the volume %q does not exist", snapshot.Spec.VolumeRef), nil
	}
	if err != nil {
		return "", nil, err
	}
	var pvc corev1.PersistentVolumeClaim
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(&volume), &pvc)
	if apierrors.IsNotFound(err) || (err == nil && pvc.Spec.StorageClassName == nil) {
		return "", gateOf(ReasonVolumeNotFound, "the claim of the volume %q does not exist yet", volume.Name), nil
	}
	if err != nil {
		return "", nil, err
	}
	var class storagev1.StorageClass
	err = r.Client.Get(ctx, client.ObjectKey{Name: *pvc.Spec.StorageClassName}, &class)
	if apierrors.IsNotFound(err) {
		return "", gateOf(ReasonNoSnapshotClass, "the storage class of the volume does not exist"), nil
	}
	return class.Provisioner, nil, err
}

func csiSnapshot(snapshot v1alpha1.VolumeSnapshot, labels map[string]string, class string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata":   map[string]any{"name": snapshot.Name, "namespace": snapshot.Namespace},
		"spec": map[string]any{
			"volumeSnapshotClassName": class,
			"source":                  map[string]any{"persistentVolumeClaimName": snapshot.Spec.VolumeRef},
		},
	}}
	obj.SetLabels(copyOf(labels))
	obj.SetOwnerReferences([]metav1.OwnerReference{controllerRef("VolumeSnapshot", &snapshot)})
	return obj
}

func (r *VolumeSnapshotReconciler) observe(ctx context.Context, snapshot v1alpha1.VolumeSnapshot) (snapshotOutcome, error) {
	csi := emptyOf(csiSnapshotGVK)
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&snapshot), csi); err != nil {
		return snapshotOutcome{}, err
	}
	if message, _, _ := unstructured.NestedString(csi.Object, "status", "error", "message"); message != "" {
		return snapshotOutcome{Phase: v1alpha1.VolumeSnapshotPhaseFailed, Message: message}, nil
	}
	if ready, _, _ := unstructured.NestedBool(csi.Object, "status", "readyToUse"); ready {
		restore, _, _ := unstructured.NestedString(csi.Object, "status", "restoreSize")
		size, _ := resource.ParseQuantity(restore)
		return snapshotOutcome{Phase: v1alpha1.VolumeSnapshotPhaseReady, SizeBytes: size.Value()}, nil
	}
	return snapshotOutcome{Phase: v1alpha1.VolumeSnapshotPhasePending}, nil
}

func snapshotCondition(snapshot v1alpha1.VolumeSnapshot, done snapshotOutcome) metav1.Condition {
	cond := metav1.Condition{Type: v1alpha1.ConditionReady, ObservedGeneration: snapshot.Generation}
	switch {
	case done.Gate != nil:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, done.Gate.Reason, done.Gate.Message
	case done.Phase == v1alpha1.VolumeSnapshotPhaseReady:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "Ready", "the snapshot is ready"
	case done.Phase == v1alpha1.VolumeSnapshotPhaseFailed:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Failed", done.Message
	default:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Pending", "the snapshot is not ready yet"
	}
	return cond
}

func (r *VolumeSnapshotReconciler) writeStatus(ctx context.Context, snapshot v1alpha1.VolumeSnapshot, done snapshotOutcome) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.VolumeSnapshot
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&snapshot), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		desired.Status.ObservedGeneration = current.Generation
		if done.Phase != "" {
			desired.Status.Phase, desired.Status.SizeBytes = done.Phase, done.SizeBytes
		} else if desired.Status.Phase == "" {
			desired.Status.Phase = v1alpha1.VolumeSnapshotPhasePending
		}
		desired.Status.Conditions = mergeConditions(current.Status.Conditions, []metav1.Condition{snapshotCondition(current, done)}, r.now())
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.ComputeFieldManager))
	})
}

func (r *VolumeSnapshotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("compute-volumesnapshot").
		For(&v1alpha1.VolumeSnapshot{}).
		Owns(emptyOf(csiSnapshotGVK)).
		Complete(r)
}
