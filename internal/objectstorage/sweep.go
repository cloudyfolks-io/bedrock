package objectstorage

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const sweepInterval = 10 * time.Minute

type Sweeper struct {
	Client client.Client
	DryRun bool
}

func (s *Sweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) {
	gvks := []schema.GroupVersionKind{cephObjectStoreAccountGVK, cephObjectStoreUserGVK}
	orphans, err := platform.SweepOrphans(ctx, s.Client, gvks, OwnerExists(s.Client), s.DryRun)
	if err != nil {
		logf.FromContext(ctx).Info("orphan sweep failed", "error", err.Error())
		return
	}
	for _, orphan := range orphans {
		logf.FromContext(ctx).Info("orphan platform object", "kind", orphan.GetKind(), "name", orphan.GetName(), "dryRun", s.DryRun)
		s.releaseGone(ctx, platform.OwnerOf(orphan))
	}
}

func OwnerExists(c client.Client) func(context.Context, platform.Owner) (bool, error) {
	return func(ctx context.Context, owner platform.Owner) (bool, error) {
		switch owner.Kind {
		case "Namespace":
			return stillOwns(ctx, c, namespaceMeta(), client.ObjectKey{Name: owner.Name}, owner)
		case "ObjectStorageUser":
			return stillOwns(ctx, c, &v1alpha1.ObjectStorageUser{}, client.ObjectKey{Namespace: owner.Namespace, Name: owner.Name}, owner)
		}
		return true, nil
	}
}

func stillOwns(ctx context.Context, c client.Client, obj client.Object, key client.ObjectKey, owner platform.Owner) (bool, error) {
	err := c.Get(ctx, key, obj)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return owner.UID == "" || obj.GetUID() == owner.UID, nil
}

func (s *Sweeper) releaseGone(ctx context.Context, owner platform.Owner) {
	namespace := owner.Namespace
	if owner.Kind == "Namespace" {
		namespace = owner.Name
	}
	if namespace == "" {
		return
	}
	err := s.Client.Get(ctx, client.ObjectKey{Name: namespace}, namespaceMeta())
	if !apierrors.IsNotFound(err) {
		return
	}
	if err := ReleaseNamespace(ctx, s.Client, namespace); err != nil {
		logf.FromContext(ctx).Info("release of a gone namespace failed", "namespace", namespace, "error", err.Error())
	}
}
