package network

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func OwnedPods(container v1alpha1.Container, deployment appsv1.Deployment, sets []appsv1.ReplicaSet, pods []corev1.Pod) []corev1.Pod {
	if !metav1.IsControlledBy(&deployment, &container) {
		return nil
	}
	var owned []corev1.Pod
	for index := range sets {
		if !metav1.IsControlledBy(&sets[index], &deployment) {
			continue
		}
		for _, pod := range pods {
			if metav1.IsControlledBy(&pod, &sets[index]) {
				owned = append(owned, pod)
			}
		}
	}
	return owned
}

func PodsOfContainer(ctx context.Context, reader client.Reader, container v1alpha1.Container) ([]corev1.Pod, error) {
	var deployment appsv1.Deployment
	err := reader.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: container.Name}, &deployment)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	selector := client.MatchingLabels{v1alpha1.LabelKind: "Container", v1alpha1.LabelName: container.Name}
	var sets appsv1.ReplicaSetList
	if err := reader.List(ctx, &sets, client.InNamespace(container.Namespace), selector); err != nil {
		return nil, err
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(container.Namespace), selector); err != nil {
		return nil, err
	}
	return OwnedPods(container, deployment, sets.Items, pods.Items), nil
}
