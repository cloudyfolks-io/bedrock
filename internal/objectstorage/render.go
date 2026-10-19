package objectstorage

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const maxBucketName = 63

func CephUser(user *v1alpha1.ObjectStorageUser, project Project) *unstructured.Unstructured {
	rgwName := RGWUserName(user.Namespace, user.Name)
	spec := map[string]any{
		"store":       StoreName,
		"displayName": rgwName,
		"accountRef":  map[string]any{"name": AccountName(user.Namespace)},
	}
	if user.Spec.Suspended {
		spec["opMask"] = []any{}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata":   map[string]any{"name": rgwName, "namespace": StoreNamespace},
		"spec":       spec,
	}}
	owner := platform.Owner{Kind: "ObjectStorageUser", Name: user.Name, Namespace: user.Namespace, UID: user.UID, Tenant: project.Tenant, Project: project.Name}
	return platform.Adopt(obj, owner)
}

func S3BucketName(bucket *v1alpha1.ObjectStorageBucket) string {
	if bucket.Spec.BucketName != "" {
		return bucket.Spec.BucketName
	}
	generated := strings.ReplaceAll(bucket.Namespace+"-"+bucket.Name, ".", "-")
	return strings.TrimRight(cut(generated, maxBucketName), "-")
}

func cut(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
