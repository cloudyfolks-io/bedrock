package objectstorage

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func bucketsInScope(ctx context.Context, c client.Client, user *v1alpha1.ObjectStorageUser) ([]string, error) {
	var buckets v1alpha1.ObjectStorageBucketList
	if err := c.List(ctx, &buckets, client.InNamespace(user.Namespace)); err != nil {
		return nil, err
	}
	named := map[string]bool{}
	for _, name := range user.Spec.Buckets {
		named[name] = true
	}
	scope := []string{}
	for i := range buckets.Items {
		bucket := &buckets.Items[i]
		if !bucket.DeletionTimestamp.IsZero() || len(named) > 0 && !named[bucket.Name] {
			continue
		}
		scope = append(scope, S3BucketName(bucket))
	}
	return scope, nil
}
