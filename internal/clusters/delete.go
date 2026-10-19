package clusters

import (
	"context"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	deleteRetry  = 5 * time.Second
	cleanupGrace = 5 * time.Minute
)

type deleteStep struct {
	Message string
	Run     func(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error)
}

func (r *Reconciler) deleteSteps() []deleteStep {
	return []deleteStep{
		{Message: "deleting the worker VMs", Run: r.deleteWorkerVMs},
		{Message: "deleting the control plane and its volume", Run: r.deleteControlPlane},
		{Message: "removing the token requests and the cloud-init secrets", Run: r.removeTokens},
	}
}

func (r *Reconciler) finalize(ctx context.Context, cluster v1alpha1.KubernetesCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&cluster, v1alpha1.KubernetesClusterFinalizer) {
		return ctrl.Result{}, nil
	}
	for _, step := range r.deleteSteps() {
		done, err := step.Run(ctx, cluster)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !done {
			if err := r.writeDeleting(ctx, cluster, step.Message); err != nil {
				return ctrl.Result{}, fmt.Errorf("write the deleting status: %w", err)
			}
			return ctrl.Result{RequeueAfter: deleteRetry}, nil
		}
	}
	return ctrl.Result{}, r.releaseFinalizer(ctx, cluster)
}

func (r *Reconciler) releaseFinalizer(ctx context.Context, cluster v1alpha1.KubernetesCluster) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.KubernetesCluster
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(&cluster), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !controllerutil.RemoveFinalizer(&current, v1alpha1.KubernetesClusterFinalizer) {
			return nil
		}
		return client.IgnoreNotFound(r.Client.Update(ctx, &current))
	})
}

func (r *Reconciler) ensureFinalizer(ctx context.Context, cluster v1alpha1.KubernetesCluster) error {
	if controllerutil.ContainsFinalizer(&cluster, v1alpha1.KubernetesClusterFinalizer) {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.KubernetesCluster
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(&cluster), &current); err != nil {
			return err
		}
		if !controllerutil.AddFinalizer(&current, v1alpha1.KubernetesClusterFinalizer) {
			return nil
		}
		return r.Client.Update(ctx, &current)
	})
}

func (r *Reconciler) writeDeleting(ctx context.Context, cluster v1alpha1.KubernetesCluster, message string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.KubernetesCluster
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(&cluster), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		desired.Status = DeletingStatus(current, message, r.now())
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.KubernetesClusterFieldManager))
	})
}

func DeletingStatus(cluster v1alpha1.KubernetesCluster, message string, now time.Time) v1alpha1.KubernetesClusterStatus {
	status := *cluster.Status.DeepCopy()
	status.ObservedGeneration = cluster.Generation
	status.Phase = v1alpha1.KubernetesClusterPhaseDeleting
	condition := metav1.Condition{
		Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonDeleting, Message: message,
		ObservedGeneration: cluster.Generation, LastTransitionTime: metav1.NewTime(now),
	}
	for _, previous := range cluster.Status.Conditions {
		if previous.Type == condition.Type && previous.Status == condition.Status {
			condition.LastTransitionTime = previous.LastTransitionTime
		}
	}
	status.Conditions = []metav1.Condition{condition}
	return status
}

func (r *Reconciler) clusterVMs(ctx context.Context, cluster v1alpha1.KubernetesCluster) ([]v1alpha1.VirtualMachine, error) {
	var list v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &list, client.InNamespace(cluster.Namespace), client.MatchingLabels{LabelCluster: cluster.Name}); err != nil {
		return nil, err
	}
	var owned []v1alpha1.VirtualMachine
	for _, vm := range list.Items {
		if metav1.IsControlledBy(&vm, &cluster) {
			owned = append(owned, vm)
		}
	}
	return owned, nil
}

func (r *Reconciler) deleteWorkerVMs(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	vms, err := r.clusterVMs(ctx, cluster)
	if err != nil {
		return false, err
	}
	for index := range vms {
		if vms[index].DeletionTimestamp.IsZero() {
			if err := client.IgnoreNotFound(r.Client.Delete(ctx, &vms[index])); err != nil {
				return false, err
			}
		}
	}
	return len(vms) == 0, nil
}

func workerNamesOf(cluster v1alpha1.KubernetesCluster, vms []v1alpha1.VirtualMachine) []string {
	names := make([]string, 0, len(cluster.Spec.Workers)+len(vms))
	for _, worker := range cluster.Spec.Workers {
		names = append(names, worker.Name)
	}
	for _, vm := range vms {
		names = append(names, vm.Labels[LabelWorker])
	}
	slices.Sort(names)
	return slices.Compact(slices.DeleteFunc(names, func(name string) bool { return name == "" }))
}

func (r *Reconciler) removeTokens(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	vms, err := r.clusterVMs(ctx, cluster)
	if err != nil {
		return false, err
	}
	requests, err := r.listTokenRequests(ctx, cluster)
	if err != nil {
		return false, err
	}
	for _, worker := range append(workerNamesOf(cluster, vms), requestWorkers(requests)...) {
		if worker == "" {
			continue
		}
		if err := r.removeWorkerLeftovers(ctx, cluster, worker); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *Reconciler) removeWorkerLeftovers(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) error {
	if err := r.forceDeleteTokenRequest(ctx, cluster, worker); err != nil {
		return err
	}
	return r.deleteCloudInit(ctx, cluster, worker)
}

func (r *Reconciler) deleteCloudInit(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: CloudInitSecretName(cluster.Name, worker)}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, secret))
}

func (r *Reconciler) tokenRequest(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) (*unstructured.Unstructured, bool, error) {
	request := &unstructured.Unstructured{}
	request.SetGroupVersionKind(joinTokenGVK)
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: WorkerName(cluster.Name, worker)}, request)
	if apierrors.IsNotFound(err) || notFound(err) {
		return nil, false, nil
	}
	return request, err == nil, err
}

func (r *Reconciler) deleteTokenRequest(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) error {
	request, found, err := r.tokenRequest(ctx, cluster, worker)
	if err != nil || !found || !request.GetDeletionTimestamp().IsZero() {
		return err
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, request))
}

func (r *Reconciler) forceDeleteTokenRequest(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) error {
	request, found, err := r.tokenRequest(ctx, cluster, worker)
	if err != nil || !found {
		return err
	}
	if len(request.GetFinalizers()) > 0 {
		base := request.DeepCopy()
		request.SetFinalizers(nil)
		if err := r.Client.Patch(ctx, request, client.MergeFrom(base)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, request))
}

func (r *Reconciler) deleteControlPlane(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	gone, err := r.deleteK0smotronCluster(ctx, cluster)
	if err != nil || !gone {
		return false, err
	}
	for _, check := range []func(context.Context, v1alpha1.KubernetesCluster) (bool, error){r.statefulSetGone, r.claimGone} {
		absent, err := check(ctx, cluster)
		if err != nil || !absent {
			return false, err
		}
	}
	return true, nil
}

func (r *Reconciler) deleteK0smotronCluster(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(clusterGVK)
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, existing)
	if apierrors.IsNotFound(err) || notFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(existing, &cluster) {
		return true, nil
	}
	if existing.GetDeletionTimestamp().IsZero() {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, existing)); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *Reconciler) statefulSetGone(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: StatefulSetName(cluster.Name)}, &appsv1.StatefulSet{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

func (r *Reconciler) claimGone(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: ControlPlaneClaimName(cluster.Name)}, &corev1.PersistentVolumeClaim{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

func requestWorkers(requests *unstructured.UnstructuredList) []string {
	names := make([]string, 0, len(requests.Items))
	for index := range requests.Items {
		names = append(names, requests.Items[index].GetLabels()[LabelWorker])
	}
	return names
}

func (r *Reconciler) listTokenRequests(ctx context.Context, cluster v1alpha1.KubernetesCluster) (*unstructured.UnstructuredList, error) {
	requests := &unstructured.UnstructuredList{}
	requests.SetGroupVersionKind(joinTokenGVK.GroupVersion().WithKind("JoinTokenRequestList"))
	err := r.Client.List(ctx, requests, client.InNamespace(cluster.Namespace), client.MatchingLabels{LabelCluster: cluster.Name})
	if err != nil && !notFound(err) {
		return nil, err
	}
	return requests, nil
}

func (r *Reconciler) staleWorkers(ctx context.Context, cluster v1alpha1.KubernetesCluster, vms []v1alpha1.VirtualMachine) ([]string, error) {
	requests, err := r.listTokenRequests(ctx, cluster)
	if err != nil {
		return nil, err
	}
	names := workerNamesOf(v1alpha1.KubernetesCluster{}, vms)
	names = append(names, requestWorkers(requests)...)
	wanted := make([]string, len(cluster.Spec.Workers))
	for i, worker := range cluster.Spec.Workers {
		wanted[i] = worker.Name
	}
	slices.Sort(names)
	names = slices.Compact(slices.DeleteFunc(names, func(name string) bool { return name == "" || slices.Contains(wanted, name) }))
	return names, nil
}

func (r *Reconciler) pruneWorkers(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, *Gate, error) {
	vms, err := r.clusterVMs(ctx, cluster)
	if err != nil {
		return false, nil, err
	}
	stale, err := r.staleWorkers(ctx, cluster, vms)
	if err != nil {
		return false, nil, err
	}
	pending := false
	var stuck *Gate
	for _, worker := range stale {
		waiting, gate, err := r.pruneWorker(ctx, cluster, worker, vms)
		if err != nil {
			return false, nil, err
		}
		pending = pending || waiting
		if stuck == nil {
			stuck = gate
		}
	}
	return pending, stuck, nil
}

func (r *Reconciler) pruneWorker(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string, vms []v1alpha1.VirtualMachine) (bool, *Gate, error) {
	index := slices.IndexFunc(vms, func(vm v1alpha1.VirtualMachine) bool { return vm.Labels[LabelWorker] == worker })
	if index >= 0 {
		if vms[index].DeletionTimestamp.IsZero() {
			return true, nil, client.IgnoreNotFound(r.Client.Delete(ctx, &vms[index]))
		}
		return true, nil, nil
	}
	if err := r.deleteTokenRequest(ctx, cluster, worker); err != nil {
		return false, nil, err
	}
	if err := r.deleteCloudInit(ctx, cluster, worker); err != nil {
		return false, nil, err
	}
	return false, r.stuckTokenRequest(ctx, cluster, worker), nil
}

func (r *Reconciler) stuckTokenRequest(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) *Gate {
	request, found, err := r.tokenRequest(ctx, cluster, worker)
	if err != nil || !found || request.GetDeletionTimestamp().IsZero() || r.now().Sub(request.GetDeletionTimestamp().Time) < cleanupGrace {
		return nil
	}
	return gateOf(ReasonWorkerCleanupPending, "the token request %s of the removed worker %q has been deleting for more than %s: check that the k0smotron operator runs", request.GetName(), worker, cleanupGrace)
}
