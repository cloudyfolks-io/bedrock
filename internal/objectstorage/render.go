package objectstorage

import (
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const maxGenerateBucketName = 50

func CephUser(user *v1alpha1.ObjectStorageUser, tenant, project string) (*unstructured.Unstructured, error) {
	quotas, err := userQuotas(user.Spec.Quota)
	if err != nil {
		return nil, err
	}
	spec := map[string]any{
		"store":            StoreName,
		"clusterNamespace": StoreNamespace,
		"displayName":      displayName(user),
	}
	if len(quotas) > 0 {
		spec["quotas"] = quotas
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

func BucketClaim(bucket *v1alpha1.ObjectStorageBucket, tenant, project string) (*unstructured.Unstructured, error) {
	config, err := bucketConfig(bucket)
	if err != nil {
		return nil, err
	}
	owner := metav1.NewControllerRef(bucket, v1alpha1.GroupVersion.WithKind("ObjectStorageBucket"))
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "objectbucket.io/v1alpha1",
		"kind":       "ObjectBucketClaim",
		"metadata":   map[string]any{"name": bucket.Name, "namespace": bucket.Namespace},
		"spec": map[string]any{
			"generateBucketName": cut(bucket.Name, maxGenerateBucketName),
			"storageClassName":   BucketClassName,
			"additionalConfig":   config,
		},
	}}
	obj.SetLabels(v1alpha1.TenantLabels("ObjectStorageBucket", bucket.Name, tenant, project))
	obj.SetOwnerReferences([]metav1.OwnerReference{*owner})
	return obj, nil
}

func bucketConfig(bucket *v1alpha1.ObjectStorageBucket) (map[string]any, error) {
	config := map[string]any{"bucketOwner": RGWUserName(bucket.Namespace, bucket.Spec.UserRef)}
	quota := bucket.Spec.Quota
	if quota.Bytes != nil && quota.Bytes.Value() < 0 {
		return nil, ErrQuotaInvalid
	}
	if quota.Objects != nil && *quota.Objects < 0 {
		return nil, ErrQuotaInvalid
	}
	if quota.Bytes != nil && quota.Bytes.Value() > 0 {
		config["bucketMaxSize"] = strconv.FormatInt(quota.Bytes.Value(), 10)
	}
	if quota.Objects != nil && *quota.Objects > 0 {
		config["bucketMaxObjects"] = strconv.FormatInt(*quota.Objects, 10)
	}
	return config, nil
}

func userQuotas(quota v1alpha1.ObjectStorageUserQuota) (map[string]any, error) {
	if quota.Bytes != nil && quota.Bytes.Value() < 0 {
		return nil, ErrQuotaInvalid
	}
	if quota.Buckets != nil && *quota.Buckets < 0 {
		return nil, ErrQuotaInvalid
	}
	quotas := map[string]any{}
	if quota.Bytes != nil && quota.Bytes.Value() > 0 {
		quotas["maxSize"] = quota.Bytes.Value()
	}
	if quota.Buckets != nil && *quota.Buckets > 0 {
		quotas["maxBuckets"] = int64(*quota.Buckets)
	}
	return quotas, nil
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
