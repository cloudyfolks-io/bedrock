package objectstorage

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const AdminUserName = "bedrock-admin"

func Store() platform.Store {
	return platform.Store{Name: StoreName, Namespace: StoreNamespace}
}

func adminCapabilities() map[string]any {
	return map[string]any{"buckets": "read, write", "usage": "read"}
}

func EnsureAdminUser(ctx context.Context, c client.Client) (s3.Credentials, bool, error) {
	return platform.EnsureStoreUser(ctx, c, Store(), AdminUserName, v1alpha1.ObjectStorageFieldManager, adminCapabilities())
}
