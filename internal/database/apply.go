package database

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func ApplyPatch(ctx context.Context, c client.Client, patch engine.Patch) error {
	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(patch.GVK)
	target.SetName(patch.Name)
	target.SetNamespace(patch.Namespace)
	if target.GetNamespace() == "" {
		target.SetNamespace(PlatformNamespace)
	}
	err := c.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch.Body), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	return client.IgnoreNotFound(err)
}

func ApplyPatches(ctx context.Context, c client.Client, patches []engine.Patch) error {
	for _, patch := range patches {
		if err := ApplyPatch(ctx, c, patch); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func ApplyPlan(ctx context.Context, c client.Client, kind string, spec engine.Spec, projectNamespace string, result Result, now time.Time) error {
	patches, err := StepPatches(kind, spec, projectNamespace, result.Steps, now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if ChangesPatchSteps(result.Steps) {
		enginePatches, err := EnginePatches(kind, spec)
		if err != nil {
			return err
		}
		patches = append(enginePatches, patches...)
	}
	if err := ApplyPatches(ctx, c, patches); err != nil {
		return err
	}
	if !result.RequestBackup {
		return nil
	}
	backup, err := PreUpgradeBackup(kind, spec, PreUpgradeBackupName(spec.Name, now))
	if err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, backup, client.FieldOwner(v1alpha1.DatabaseFieldManager)))
}
