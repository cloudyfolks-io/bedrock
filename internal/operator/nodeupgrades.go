package operator

import (
	"context"
	"fmt"
	"slices"
	"strings"

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

func readNodeUpgrade(ctx context.Context, c client.Client, name string) (v1alpha1.NodeUpgrade, error) {
	var upgrade v1alpha1.NodeUpgrade
	err := c.Get(ctx, client.ObjectKey{Name: name}, &upgrade)
	if errors.IsNotFound(err) {
		return v1alpha1.NodeUpgrade{}, nil
	}
	return upgrade, err
}

func currentStep(upgrade v1alpha1.NodeUpgrade, name string) v1alpha1.NodeUpgradeStepStatus {
	pending := v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepPending}
	index := slices.IndexFunc(upgrade.Status.Steps, func(step v1alpha1.NodeUpgradeStepStatus) bool { return step.Name == name })
	if index < 0 {
		return pending
	}
	step := upgrade.Status.Steps[index]
	if step.State == v1alpha1.StepFailed && step.Attempt < upgrade.Spec.Attempt {
		return pending
	}
	return step
}

type stepSummary struct {
	Total     int
	Succeeded int
	Waiting   []string
	Failed    []string
}

func summarizeStep(upgrades []v1alpha1.NodeUpgrade, nodes []string, name string) stepSummary {
	summary := stepSummary{Total: len(nodes)}
	for _, node := range nodes {
		index := slices.IndexFunc(upgrades, func(upgrade v1alpha1.NodeUpgrade) bool { return upgrade.Spec.Node == node })
		if index < 0 {
			summary.Waiting = append(summary.Waiting, node)
			continue
		}
		switch step := currentStep(upgrades[index], name); step.State {
		case v1alpha1.StepSucceeded:
			summary.Succeeded++
		case v1alpha1.StepFailed:
			summary.Failed = append(summary.Failed, node+": "+step.Message)
		default:
			summary.Waiting = append(summary.Waiting, node)
		}
	}
	return summary
}

func stepResult(label string, summary stepSummary) phaseResult {
	switch {
	case len(summary.Failed) > 0:
		return phaseResult{Failure: fmt.Sprintf("%s failed on %s", label, strings.Join(summary.Failed, "; "))}
	case summary.Succeeded == summary.Total:
		return phaseResult{Done: true}
	}
	return phaseResult{Message: fmt.Sprintf("%s %d/%d nodes, waiting for %s", label, summary.Succeeded, summary.Total, strings.Join(summary.Waiting, ", "))}
}
