package secret

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func Object(owner client.Object, name string, data map[string][]byte) *corev1.Secret {
	gvk := owner.GetObjectKind().GroupVersionKind()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: release.SystemNamespace,
			Name:      name,
			Labels: map[string]string{
				v1alpha1.LabelAuthn: "true",
				v1alpha1.LabelKind:  gvk.Kind,
				v1alpha1.LabelName:  v1alpha1.LabelValue(owner.GetName()),
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(owner, gvk),
			},
		},
		Data: data,
	}
}
