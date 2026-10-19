package objectstorage

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const maxGenerateBucketName = 50

func CephUser(user *v1alpha1.ObjectStorageUser, tenant, project string) (*unstructured.Unstructured, error) {
	spec := map[string]any{
		"store":            StoreName,
		"clusterNamespace": StoreNamespace,
		"displayName":      displayName(user),
	}
	owner := metav1.NewControllerRef(user, v1alpha1.GroupVersion.WithKind("ObjectStorageUser"))
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata":   map[string]any{"name": RGWUserName(user.Namespace, user.Name), "namespace": user.Namespace},
		"spec":       spec,
	}}
	obj.SetLabels(v1alpha1.TenantLabels("ObjectStorageUser", user.Name, tenant, project))
	obj.SetOwnerReferences([]metav1.OwnerReference{*owner})
	return obj, nil
}

func displayName(user *v1alpha1.ObjectStorageUser) string {
	if user.Spec.DisplayName != "" {
		return user.Spec.DisplayName
	}
	return user.Namespace + "/" + user.Name
}

func cut(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
