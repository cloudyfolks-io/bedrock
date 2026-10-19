package container

import (
	"context"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func loadContainer(ctx context.Context, c client.Client, key client.ObjectKey) (v1alpha1.Container, error) {
	var container v1alpha1.Container
	if len(validation.IsDNS1123Label(key.Namespace)) > 0 || len(validation.IsDNS1123Label(key.Name)) > 0 {
		return container, badRequest("the namespace and the name must be DNS labels")
	}
	err := c.Get(ctx, key, &container)
	if apierrors.IsNotFound(err) {
		return container, notFoundError("the container %q does not exist", key.Name)
	}
	if err != nil {
		return container, internalError(err)
	}
	return container, nil
}

func podsOf(ctx context.Context, c client.Client, key client.ObjectKey) ([]corev1.Pod, error) {
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(key.Namespace), client.MatchingLabels(SelectorLabels(key.Name))); err != nil {
		return nil, internalError(err)
	}
	pods := slices.DeleteFunc(list.Items, func(pod corev1.Pod) bool { return !pod.DeletionTimestamp.IsZero() })
	slices.SortFunc(pods, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	return pods, nil
}

func PickPods(pods []corev1.Pod, requested string) ([]corev1.Pod, error) {
	if len(pods) == 0 {
		return nil, conflict("the container has no pod")
	}
	if requested == "" {
		return pods, nil
	}
	for _, pod := range pods {
		if pod.Name == requested {
			return []corev1.Pod{pod}, nil
		}
	}
	return nil, notFoundError("the pod %q does not belong to this container", requested)
}

func PickReadyPod(pods []corev1.Pod, requested string) (corev1.Pod, error) {
	candidates, err := PickPods(pods, requested)
	if err != nil {
		return corev1.Pod{}, err
	}
	for _, pod := range candidates {
		if network.PodReady(pod) {
			return pod, nil
		}
	}
	return corev1.Pod{}, conflict("the container has no ready pod")
}
