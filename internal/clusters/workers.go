package clusters

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func (r *Reconciler) ownedVM(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) (v1alpha1.VirtualMachine, bool, error) {
	var vm v1alpha1.VirtualMachine
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: WorkerName(cluster.Name, worker)}, &vm)
	if apierrors.IsNotFound(err) {
		return vm, false, nil
	}
	if err != nil {
		return vm, false, err
	}
	if !metav1.IsControlledBy(&vm, &cluster) {
		return vm, false, nameConflict{Kind: "VirtualMachine", Name: vm.Name}
	}
	return vm, true, nil
}

func (r *Reconciler) syncWorkers(ctx context.Context, cluster v1alpha1.KubernetesCluster, labels map[string]string) (outcome, error) {
	if err := r.pruneWorkers(ctx, cluster); err != nil {
		return outcome{}, err
	}
	var pending *v1alpha1.KubernetesWorker
	for index := range cluster.Spec.Workers {
		worker := cluster.Spec.Workers[index]
		vm, found, err := r.ownedVM(ctx, cluster, worker.Name)
		if err != nil {
			return outcome{}, err
		}
		if !found {
			if pending == nil {
				pending = &cluster.Spec.Workers[index]
			}
			continue
		}
		if err := r.maintainWorker(ctx, cluster, worker, vm, labels); err != nil {
			return outcome{}, err
		}
	}
	if pending == nil {
		return outcome{}, nil
	}
	waiting, err := r.provisionWorker(ctx, cluster, *pending, labels)
	if err != nil {
		return outcome{}, err
	}
	if waiting {
		return outcome{RequeueAfter: provisionRetry}, nil
	}
	return outcome{RequeueAfter: time.Second}, nil
}

func (r *Reconciler) maintainWorker(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker v1alpha1.KubernetesWorker, vm v1alpha1.VirtualMachine, labels map[string]string) error {
	desired, err := RenderWorker(WorkerInput{Cluster: cluster, Worker: worker, Labels: labels})
	if err != nil {
		return err
	}
	if err := ssa.Apply(ctx, r.Client, desired, v1alpha1.KubernetesClusterFieldManager); err != nil {
		return fmt.Errorf("apply the worker %s: %w", worker.Name, err)
	}
	if vm.Status.Phase != vmPhaseRunning {
		return nil
	}
	return r.releaseToken(ctx, cluster, worker.Name)
}

func (r *Reconciler) releaseToken(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string) error {
	request := &unstructured.Unstructured{}
	request.SetGroupVersionKind(joinTokenGVK)
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: WorkerName(cluster.Name, worker)}, request)
	if apierrors.IsNotFound(err) || notFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.now().Sub(request.GetCreationTimestamp().Time) < tokenGrace {
		return nil
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, request))
}

func (r *Reconciler) provisionWorker(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker v1alpha1.KubernetesWorker, labels map[string]string) (bool, error) {
	token, ready, err := r.joinToken(ctx, cluster, worker.Name, labels)
	if err != nil || !ready {
		return true, err
	}
	secret := RenderCloudInitSecret(cluster, worker.Name, token, labels)
	if err := ssa.Apply(ctx, r.Client, secret, v1alpha1.KubernetesClusterFieldManager); err != nil {
		return false, fmt.Errorf("apply the cloud-init secret of %s: %w", worker.Name, err)
	}
	desired, err := RenderWorker(WorkerInput{Cluster: cluster, Worker: worker, Labels: labels})
	if err != nil {
		return false, err
	}
	if err := ssa.Apply(ctx, r.Client, desired, v1alpha1.KubernetesClusterFieldManager); err != nil {
		return false, fmt.Errorf("apply the worker %s: %w", worker.Name, err)
	}
	return false, nil
}

func (r *Reconciler) joinToken(ctx context.Context, cluster v1alpha1.KubernetesCluster, worker string, labels map[string]string) (string, bool, error) {
	request := &unstructured.Unstructured{}
	request.SetGroupVersionKind(joinTokenGVK)
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: WorkerName(cluster.Name, worker)}
	err := r.Client.Get(ctx, key, request)
	if apierrors.IsNotFound(err) {
		return "", false, r.Client.Create(ctx, RenderToken(cluster, worker, labels), client.FieldOwner(v1alpha1.KubernetesClusterFieldManager))
	}
	if err != nil {
		return "", false, err
	}
	if tokenID, _, _ := unstructured.NestedString(request.Object, "status", "tokenID"); tokenID == "" {
		return "", false, nil
	}
	var secret corev1.Secret
	err = r.secretReader().Get(ctx, key, &secret)
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	token := string(secret.Data["token"])
	return token, token != "", err
}
