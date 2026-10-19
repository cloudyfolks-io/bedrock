package registry

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

var (
	errStorePending = errors.New("the object store identity is not ready")
	errNotOwned     = errors.New("the bucket exists and another identity owns it")
)

type storeSpec struct {
	identity platform.Identity
	secret   string
	accessID string
	bucket   string
}

func imageStore(config Config) storeSpec {
	return storeSpec{
		identity: platform.Identity{
			Store:           objectstorage.Store(),
			Name:            storeIdentityName,
			TargetNamespace: Namespace,
			AccessKeyNames:  []string{"REGISTRY_STORAGE_S3_ACCESSKEY"},
			SecretKeyNames:  []string{"REGISTRY_STORAGE_S3_SECRETKEY"},
			Manager:         v1alpha1.RegistryFieldManager,
		},
		accessID: "REGISTRY_STORAGE_S3_ACCESSKEY",
		secret:   "REGISTRY_STORAGE_S3_SECRETKEY",
		bucket:   config.Bucket,
	}
}

func backupStore(config Config) storeSpec {
	return storeSpec{
		identity: platform.Identity{
			Store:           objectstorage.Store(),
			Name:            backupIdentityName,
			TargetNamespace: Namespace,
			AccessKeyNames:  []string{"ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"},
			SecretKeyNames:  []string{"ACCESS_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"},
			Manager:         v1alpha1.RegistryFieldManager,
		},
		accessID: "ACCESS_KEY_ID",
		secret:   "ACCESS_SECRET_KEY",
		bucket:   config.BackupBucket,
	}
}

func ensureStore(ctx context.Context, c client.Client, store *s3.Client, endpoint string, spec storeSpec) error {
	ready, err := platform.EnsureStoreIdentity(ctx, c, spec.identity)
	if err != nil {
		return err
	}
	if !ready {
		return errStorePending
	}
	var target corev1.Secret
	err = c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: spec.identity.Name}, &target)
	if apierrors.IsNotFound(err) {
		return errStorePending
	}
	if err != nil {
		return err
	}
	keys := s3.Credentials{AccessKey: string(target.Data[spec.accessID]), SecretKey: string(target.Data[spec.secret])}
	err = platform.EnsureStoreBucket(ctx, store, endpoint, objectstorage.Region, keys, spec.bucket)
	if s3.IsCode(err, s3.BucketAlreadyExists) {
		return errNotOwned
	}
	return err
}
