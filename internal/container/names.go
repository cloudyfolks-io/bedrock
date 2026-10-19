package container

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	Kind             = "Container"
	appContainerName = "app"
)

func SelectorLabels(name string) map[string]string {
	return map[string]string{v1alpha1.LabelKind: Kind, v1alpha1.LabelName: name}
}

func OwnerLabels(name string, ns corev1.Namespace) (map[string]string, bool) {
	tenant, project := ns.Labels[v1alpha1.LabelTenant], ns.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return nil, false
	}
	return v1alpha1.TenantLabels(Kind, name, tenant, project), true
}

func OwnerRef(c v1alpha1.Container) metav1.OwnerReference {
	controller, block := true, true
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               Kind,
		Name:               c.Name,
		UID:                c.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &block,
	}
}

func IsPinned(spec v1alpha1.ContainerSpec) bool {
	return spec.Network.Address != ""
}
