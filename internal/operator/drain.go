package operator

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	vmNameLabel = "vm.kubevirt.io/name"
	drainEvict  = "evict"
	drainDelete = "delete"
)

func drainPods(pods []corev1.Pod, node string) []corev1.Pod {
	var draining []corev1.Pod
	for _, pod := range pods {
		if pod.Spec.NodeName == node && !daemonSetPod(pod) && !mirrorPod(pod) && !finishedPod(pod) {
			draining = append(draining, pod)
		}
	}
	return draining
}

func evictablePods(pods []corev1.Pod) []corev1.Pod {
	var evictable []corev1.Pod
	for _, pod := range pods {
		if pod.DeletionTimestamp == nil {
			evictable = append(evictable, pod)
		}
	}
	return evictable
}

func daemonSetPod(pod corev1.Pod) bool {
	return slices.ContainsFunc(pod.OwnerReferences, func(ref metav1.OwnerReference) bool { return ref.Kind == "DaemonSet" })
}

func mirrorPod(pod corev1.Pod) bool {
	_, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]
	return ok
}

func finishedPod(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func drainAction(pod corev1.Pod, vmis, vms []unstructured.Unstructured) string {
	name := pod.Labels[vmNameLabel]
	if name == "" {
		return drainEvict
	}
	index := slices.IndexFunc(vmis, func(instance unstructured.Unstructured) bool {
		return instance.GetNamespace() == pod.Namespace && instance.GetName() == name
	})
	if index < 0 {
		return drainEvict
	}
	if migratable, _ := liveMigratable(vmis[index]); migratable || !allowsShutdown(vms, pod.Namespace, name) {
		return drainEvict
	}
	return drainDelete
}

func setUnschedulable(ctx context.Context, c client.Client, name string, unschedulable bool) error {
	var node corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
		return err
	}
	if node.Spec.Unschedulable == unschedulable {
		return nil
	}
	patched := node.DeepCopy()
	patched.Spec.Unschedulable = unschedulable
	return c.Patch(ctx, patched, client.MergeFrom(&node))
}

func drainNode(ctx context.Context, c client.Client, node string) ([]string, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods); err != nil {
		return nil, err
	}
	vmis, err := listKind(ctx, c, vmiGVK)
	if err != nil {
		return nil, err
	}
	vms, err := listKind(ctx, c, vmGVK)
	if err != nil {
		return nil, err
	}
	draining := drainPods(pods.Items, node)
	for _, pod := range evictablePods(draining) {
		if err := removePod(ctx, c, pod, drainAction(pod, vmis, vms)); err != nil {
			return nil, err
		}
	}
	return qualifiedNames(draining), nil
}

func removePod(ctx context.Context, c client.Client, pod corev1.Pod, action string) error {
	var err error
	if action == drainDelete {
		err = c.Delete(ctx, &pod)
	} else {
		err = c.SubResource("eviction").Create(ctx, &pod, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}})
	}
	if err != nil && !apierrors.IsTooManyRequests(err) && !apierrors.IsNotFound(err) {
		return fmt.Errorf("%s %s/%s: %w", action, pod.Namespace, pod.Name, err)
	}
	return nil
}

func qualifiedNames(pods []corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Namespace+"/"+pod.Name)
	}
	slices.Sort(names)
	return names
}
