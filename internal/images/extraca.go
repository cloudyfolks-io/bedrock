package images

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const settingAdditionalCA = "platform.additional-ca"

func AdditionalCAPEM(ctx context.Context, c client.Reader) (string, error) {
	return settingValue(ctx, c, settingAdditionalCA)
}

var ErrForeignConfigMap = errors.New("a ConfigMap that Bedrock does not manage has the same name")

func ApplyAdditionalCA(ctx context.Context, c client.Client, namespace, pem string) error {
	var existing corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: AdditionalCAConfigMap}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		return c.Create(ctx, additionalCAConfigMap(namespace, pem), client.FieldOwner(v1alpha1.ImagesFieldManager))
	case err != nil:
		return err
	case !ownedByImages(existing):
		return fmt.Errorf("configmap %s/%s: %w", namespace, AdditionalCAConfigMap, ErrForeignConfigMap)
	case existing.Data[AdditionalCAKey] == pem:
		return nil
	}
	existing.Data = map[string]string{AdditionalCAKey: pem}
	return c.Update(ctx, &existing, client.FieldOwner(v1alpha1.ImagesFieldManager))
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

func syncAdditionalCA(ctx context.Context, c client.Client, namespace, pem string) (string, error) {
	if pem == "" {
		return "", RemoveAdditionalCA(ctx, c, namespace)
	}
	err := ApplyAdditionalCA(ctx, c, namespace, pem)
	if errors.Is(err, ErrForeignConfigMap) {
		return err.Error(), nil
	}
	return "", err
}
