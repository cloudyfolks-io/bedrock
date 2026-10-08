package network

import (
	"context"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func patchFinalizer(ctx context.Context, c client.Client, obj client.Object, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := obj.DeepCopyObject().(client.Object)
		if !change(obj, Finalizer) {
			return nil
		}
		return c.Patch(ctx, obj, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.NetworkFieldManager))
	})
}

func ensureFinalizer(ctx context.Context, c client.Client, obj client.Object) error {
	return patchFinalizer(ctx, c, obj, controllerutil.AddFinalizer)
}

func releaseFinalizer(ctx context.Context, c client.Client, obj client.Object) error {
	return patchFinalizer(ctx, c, obj, controllerutil.RemoveFinalizer)
}
