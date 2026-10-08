package images

import (
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	KindImage        = "Image"
	KindClusterImage = "ClusterImage"
)

type Subject struct {
	Kind      string
	Name      string
	Namespace string
	Labels    map[string]string
	Owner     metav1.OwnerReference
	Spec      v1alpha1.ImageSpec
	ExtraCA   bool
}

func SubjectOfImage(img v1alpha1.Image, tenant, project string, extraCA bool) Subject {
	return Subject{
		Kind:      KindImage,
		Name:      img.Name,
		Namespace: img.Namespace,
		Labels:    imageLabels(img.Name, tenant, project),
		Owner:     controllerReference(KindImage, img.Name, img.UID),
		Spec:      img.Spec,
		ExtraCA:   extraCA,
	}
}

func SubjectOfClusterImage(img v1alpha1.ClusterImage, extraCA bool) Subject {
	return Subject{
		Kind:      KindClusterImage,
		Name:      img.Name,
		Namespace: CatalogNamespace,
		Labels: map[string]string{
			v1alpha1.LabelKind: KindClusterImage,
			v1alpha1.LabelName: v1alpha1.LabelValue(img.Name),
		},
		Owner:   controllerReference(KindClusterImage, img.Name, img.UID),
		Spec:    img.Spec,
		ExtraCA: extraCA,
	}
}

func imageLabels(name, tenant, project string) map[string]string {
	labels := v1alpha1.TenantLabels(KindImage, v1alpha1.LabelValue(name), tenant, project)
	if tenant == "" {
		delete(labels, v1alpha1.LabelTenant)
	}
	return maps.Clone(labels)
}

func controllerReference(kind, name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               kind,
		Name:               name,
		UID:                uid,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}
