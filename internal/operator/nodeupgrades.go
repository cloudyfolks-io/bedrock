package operator

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

func listNodeUpgrades(ctx context.Context, c client.Client, version string) ([]v1alpha1.NodeUpgrade, error) {
	var list v1alpha1.NodeUpgradeList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	var matching []v1alpha1.NodeUpgrade
	for _, upgrade := range list.Items {
		if upgrade.Spec.Version == version {
			matching = append(matching, upgrade)
		}
	}
	return matching, nil
}

func applyNodeUpgrade(ctx context.Context, c client.Client, want v1alpha1.NodeUpgrade) error {
	var current v1alpha1.NodeUpgrade
	err := c.Get(ctx, client.ObjectKey{Name: want.Name}, &current)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	spec := want.Spec
	if err == nil {
		spec = mergeNodeUpgradeSpec(current.Spec, want.Spec)
	}
	obj := &v1alpha1.NodeUpgrade{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "NodeUpgrade"},
		ObjectMeta: metav1.ObjectMeta{Name: want.Name},
		Spec:       spec,
	}
	return ssa.Apply(ctx, c, obj, v1alpha1.OperatorFieldManager)
}

func mergeNodeUpgradeSpec(current, want v1alpha1.NodeUpgradeSpec) v1alpha1.NodeUpgradeSpec {
	merged := *current.DeepCopy()
	merged.Steps = appendMissing(current.Steps, want.Steps)
	merged.Attempt = max(current.Attempt, want.Attempt)
	if current.Backup == "" {
		merged.Backup = want.Backup
	}
	return merged
}

func appendMissing(have, add []string) []string {
	merged := slices.Clone(have)
	for _, item := range add {
		if !slices.Contains(merged, item) {
			merged = append(merged, item)
		}
	}
	return merged
}

func raiseAttempts(ctx context.Context, c client.Client, version string, attempt int32) error {
	upgrades, err := listNodeUpgrades(ctx, c, version)
	if err != nil {
		return err
	}
	for _, upgrade := range upgrades {
		spec := *upgrade.Spec.DeepCopy()
		spec.Attempt = attempt
		if err := applyNodeUpgrade(ctx, c, v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: upgrade.Name}, Spec: spec}); err != nil {
			return err
		}
	}
	return nil
}
