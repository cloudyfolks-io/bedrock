package images

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const settingAdditionalCA = "platform.additional-ca"

func AdditionalCAPEM(ctx context.Context, c client.Reader) (string, error) {
	return settingValue(ctx, c, settingAdditionalCA)
}

func ApplyAdditionalCA(ctx context.Context, c client.Client, namespace, pem string) error {
	return ssa.Apply(ctx, c, additionalCAConfigMap(namespace, pem), v1alpha1.ImagesFieldManager)
}

func RemoveAdditionalCA(ctx context.Context, c client.Client, namespace string) error {
	var existing corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: AdditionalCAConfigMap}, &existing); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !ownedByImages(existing) {
		return nil
	}
	if err := c.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func additionalCAConfigMap(namespace, pem string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      AdditionalCAConfigMap,
			Namespace: namespace,
			Labels:    additionalCALabels(),
		},
		Data: map[string]string{AdditionalCAKey: pem},
	}
}

func additionalCALabels() map[string]string {
	return map[string]string{
		v1alpha1.LabelKind: "ConfigMap",
		v1alpha1.LabelName: AdditionalCAConfigMap,
	}
}

func ownedByImages(cm corev1.ConfigMap) bool {
	for key, value := range additionalCALabels() {
		if cm.Labels[key] != value {
			return false
		}
	}
	return true
}

func syncAdditionalCA(ctx context.Context, c client.Client, namespace, pem string) error {
	if pem == "" {
		return RemoveAdditionalCA(ctx, c, namespace)
	}
	return ApplyAdditionalCA(ctx, c, namespace, pem)
}
