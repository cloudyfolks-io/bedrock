package clusters

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func (r *Reconciler) observe(ctx context.Context, cluster v1alpha1.KubernetesCluster) (Observed, error) {
	controlPlane, err := r.observeControlPlane(ctx, cluster)
	if err != nil {
		return Observed{}, err
	}
	observed := Observed{ControlPlane: controlPlane, VMNames: map[string]string{}}
	if controlPlane.Ready {
		observed.KubeconfigSecret = KubeconfigSecretName(cluster.Name)
	}
	for _, worker := range cluster.Spec.Workers {
		view := WorkerView{Name: worker.Name}
		vm, found, err := r.ownedVM(ctx, cluster, worker.Name)
		var conflict nameConflict
		if err != nil && !errors.As(err, &conflict) {
			return Observed{}, err
		}
		if found {
			view.Exists, view.Phase = true, vm.Status.Phase
			observed.VMNames[worker.Name] = vm.Name
		}
		observed.Workers = append(observed.Workers, view)
	}
	return observed, nil
}

func (r *Reconciler) observeControlPlane(ctx context.Context, cluster v1alpha1.KubernetesCluster) (ControlPlaneView, error) {
	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(clusterGVK)
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, desired)
	if apierrors.IsNotFound(err) || notFound(err) {
		return ControlPlaneView{}, nil
	}
	if err != nil {
		return ControlPlaneView{}, err
	}
	ready, _, _ := unstructured.NestedInt64(desired.Object, "status", "readyReplicas")
	if ready < 1 {
		return ControlPlaneView{}, nil
	}
	var pod corev1.Pod
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: ControlPlanePodName(cluster.Name)}, &pod)
	if apierrors.IsNotFound(err) {
		return ControlPlaneView{}, nil
	}
	if err != nil {
		return ControlPlaneView{}, err
	}
	if !podReady(pod) {
		return ControlPlaneView{}, nil
	}
	exists, err := r.kubeconfigExists(ctx, cluster)
	return ControlPlaneView{Ready: exists}, err
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *Reconciler) kubeconfigExists(ctx context.Context, cluster v1alpha1.KubernetesCluster) (bool, error) {
	err := r.secretReader().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: KubeconfigSecretName(cluster.Name)}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
