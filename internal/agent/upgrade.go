package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

type Outcome struct {
	Message string
	Restart bool
	Reboot  bool
}

type StepEnv struct {
	Deps    Deps
	Client  client.Client
	Upgrade v1alpha1.NodeUpgrade
	Target  v1alpha1.Release
	From    v1alpha1.Release
}

type Step func(ctx context.Context, env StepEnv) (Outcome, error)

type stepDecision int

const (
	decisionRun stepDecision = iota
	decisionSkip
	decisionBlock
	decisionFinishReboot
)

func Steps() map[string]Step {
	return map[string]Step{
		v1alpha1.StepPreload:     preload,
		v1alpha1.StepBackup:      backup,
		v1alpha1.StepK0sUpdate:   k0sUpdate,
		v1alpha1.StepAgentUpdate: agentUpdate,
		v1alpha1.StepOSUpdate:    osUpdate,
		v1alpha1.StepReboot:      reboot,
		v1alpha1.StepPrune:       prune,
		v1alpha1.StepCleanup:     cleanup,
		v1alpha1.StepRestore:     restore,
	}
}

func RunUpgrades(ctx context.Context, c client.Client, deps Deps, steps map[string]Step) error {
	var list v1alpha1.NodeUpgradeList
	selector := client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.node", deps.Node)}
	if err := c.List(ctx, &list, selector); err != nil {
		return err
	}
	upgrades := slices.Clone(list.Items)
	slices.SortFunc(upgrades, func(a, b v1alpha1.NodeUpgrade) int { return strings.Compare(a.Name, b.Name) })
	bootID := readBootID(deps.Root)
	restored := pastRestore(readRestoreMarker(deps.Root), deps.Now())
	for _, upgrade := range upgrades {
		stopped, err := runUpgrade(ctx, c, deps, steps, upgrade, bootID, restored)
		if err != nil || stopped {
			return err
		}
	}
	return nil
}

func runUpgrade(ctx context.Context, c client.Client, deps Deps, steps map[string]Step, upgrade v1alpha1.NodeUpgrade, bootID string, restored *v1alpha1.RestoreStatus) (bool, error) {
	env, envErr := stepEnv(ctx, c, deps, upgrade)
	status := *upgrade.Status.DeepCopy()
	status.ObservedGeneration = upgrade.Generation
	attempt := upgrade.Spec.Attempt
	blocked := false
	for _, name := range upgrade.Spec.Steps {
		current := stepEntry(status, name)
		decision := stepAction(current, name, attempt, status.BootID, bootID, restored)
		if decision == decisionSkip {
			continue
		}
		if decision == decisionBlock {
			blocked = true
			continue
		}
		if decision == decisionFinishReboot {
			finished := metav1.NewTime(deps.Now())
			status = withStep(status, v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepSucceeded, Attempt: current.Attempt, Message: "rebooted", StartedAt: current.StartedAt, FinishedAt: &finished})
			if err := writeUpgradeStatus(ctx, c, upgrade.Name, status); err != nil {
				return false, err
			}
			continue
		}
		if blocked && !alwaysRuns(name) {
			continue
		}
		started := metav1.NewTime(deps.Now())
		status = withStep(status, v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepRunning, Attempt: attempt, StartedAt: &started})
		if err := writeUpgradeStatus(ctx, c, upgrade.Name, status); err != nil {
			return false, err
		}
		outcome, err := execute(ctx, steps, name, env, envErr)
		if restoredSince(name, deps.Root, started) {
			return true, err
		}
		finished := metav1.NewTime(deps.Now())
		if err != nil {
			blocked = true
			status = withStep(status, v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepFailed, Attempt: attempt, Message: err.Error(), StartedAt: &started, FinishedAt: &finished})
			if err := writeUpgradeStatus(ctx, c, upgrade.Name, status); err != nil {
				return false, err
			}
			continue
		}
		if outcome.Reboot {
			status.BootID = bootID
			status = withStep(status, v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepRunning, Attempt: attempt, Message: "rebooting", StartedAt: &started})
			if err := writeUpgradeStatus(ctx, c, upgrade.Name, status); err != nil {
				return false, err
			}
			_, err := deps.Exec.Run(ctx, "systemctl", "reboot")
			return true, err
		}
		status = withStep(status, v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepSucceeded, Attempt: attempt, Message: outcome.Message, StartedAt: &started, FinishedAt: &finished})
		if err := writeUpgradeStatus(ctx, c, upgrade.Name, status); err != nil {
			return false, err
		}
		if outcome.Restart {
			_, err := deps.Exec.Run(ctx, "systemctl", "restart", "--no-block", "bedrock-agent.service")
			return true, err
		}
	}
	return false, writeUpgradeStatus(ctx, c, upgrade.Name, status)
}

func restoredSince(step, root string, started metav1.Time) bool {
	marker := readRestoreMarker(root)
	return step == v1alpha1.StepRestore && marker != nil && !marker.CompletedAt.Time.Before(started.Time.Truncate(time.Second))
}

func stepAction(current v1alpha1.NodeUpgradeStepStatus, name string, attempt int32, recordedBoot, bootID string, restored *v1alpha1.RestoreStatus) stepDecision {
	switch {
	case current.State == v1alpha1.StepSucceeded:
		return decisionSkip
	case current.State == v1alpha1.StepRunning && startedBeforeRestore(current, restored):
		return decisionBlock
	case current.State == v1alpha1.StepRunning && name == v1alpha1.StepReboot && recordedBoot != "" && recordedBoot != bootID:
		return decisionFinishReboot
	case current.State == v1alpha1.StepFailed && current.Attempt >= attempt:
		return decisionBlock
	}
	return decisionRun
}

func pastRestore(marker *v1alpha1.RestoreStatus, now time.Time) *v1alpha1.RestoreStatus {
	if marker == nil || marker.CompletedAt.After(now) {
		return nil
	}
	return marker
}

func startedBeforeRestore(step v1alpha1.NodeUpgradeStepStatus, restored *v1alpha1.RestoreStatus) bool {
	return restored != nil && step.StartedAt.Before(&restored.CompletedAt)
}

func alwaysRuns(name string) bool {
	return name == v1alpha1.StepCleanup || name == v1alpha1.StepRestore
}

func execute(ctx context.Context, steps map[string]Step, name string, env StepEnv, envErr error) (Outcome, error) {
	if envErr != nil {
		return Outcome{}, envErr
	}
	step, ok := steps[name]
	if !ok {
		return Outcome{}, fmt.Errorf("this agent has no step %s", name)
	}
	return step(ctx, env)
}

func stepEnv(ctx context.Context, c client.Client, deps Deps, upgrade v1alpha1.NodeUpgrade) (StepEnv, error) {
	env := StepEnv{Deps: deps, Client: c, Upgrade: upgrade}
	if err := c.Get(ctx, client.ObjectKey{Name: upgrade.Spec.Version}, &env.Target); err != nil {
		return env, fmt.Errorf("release %s: %w", upgrade.Spec.Version, err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: upgrade.Spec.From}, &env.From); err != nil {
		return env, fmt.Errorf("release %s: %w", upgrade.Spec.From, err)
	}
	return env, nil
}

func stepEntry(status v1alpha1.NodeUpgradeStatus, name string) v1alpha1.NodeUpgradeStepStatus {
	for _, step := range status.Steps {
		if step.Name == name {
			return step
		}
	}
	return v1alpha1.NodeUpgradeStepStatus{Name: name}
}

func withStep(status v1alpha1.NodeUpgradeStatus, entry v1alpha1.NodeUpgradeStepStatus) v1alpha1.NodeUpgradeStatus {
	next := *status.DeepCopy()
	for i := range next.Steps {
		if next.Steps[i].Name == entry.Name {
			next.Steps[i] = entry
			return next
		}
	}
	next.Steps = append(next.Steps, entry)
	return next
}

func writeUpgradeStatus(ctx context.Context, c client.Client, name string, status v1alpha1.NodeUpgradeStatus) error {
	desired := &v1alpha1.NodeUpgrade{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "NodeUpgrade"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     status,
	}
	return ssa.ApplyStatus(ctx, c, desired, v1alpha1.AgentFieldManager)
}

func readBootID(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
