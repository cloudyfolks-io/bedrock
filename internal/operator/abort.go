package operator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const restoreRunbook = "docs/runbooks/restore-control-plane.md"

type actionFunc func(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) error

type upgradeRole struct {
	Phases  map[string]phaseFunc
	Abort   actionFunc
	Restore actionFunc
}

func oldRole() upgradeRole {
	return upgradeRole{Phases: oldPhases(), Abort: abortCleanup, Restore: awaitRestore}
}

func newRole() upgradeRole {
	return upgradeRole{Phases: newPhases(), Abort: abortHandoff, Restore: restoreControlPlane}
}

func restoreFinished(hosts []v1alpha1.Host, upgrade *v1alpha1.UpgradeStatus) bool {
	if upgrade == nil {
		return false
	}
	for _, host := range hosts {
		if restore := host.Status.Restore; restore != nil && restore.CompletedAt.After(upgrade.StartedAt.Time) {
			return true
		}
	}
	return false
}

func parseBackupLocation(location string) (string, string, bool) {
	rest, ok := strings.CutPrefix(location, "host:")
	if !ok {
		return "", "", false
	}
	node, path, ok := strings.Cut(rest, ":")
	if !ok || node == "" || !filepath.IsAbs(path) {
		return "", "", false
	}
	return node, path, true
}

func abortUpgrade(status v1alpha1.ClusterStatus, message string, generation int64) v1alpha1.ClusterStatus {
	if progressing := meta.FindStatusCondition(status.Conditions, v1alpha1.ConditionProgressing); status.Upgrade == nil && progressing != nil && progressing.Status == metav1.ConditionFalse && progressing.Reason == v1alpha1.ReasonAborted {
		return status
	}
	next := *status.DeepCopy()
	next.Phase = v1alpha1.PhaseIdle
	next.Upgrade = nil
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonAborted, message, generation)
	setCondition(&next, v1alpha1.ConditionUpgradeBlocked, metav1.ConditionFalse, v1alpha1.ReasonAborted, "", generation)
	return next
}

func restoring(status v1alpha1.ClusterStatus, message string, generation int64) v1alpha1.ClusterStatus {
	next := withMessage(status, message)
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonRestoring, message, generation)
	return next
}

func restoreManual(status v1alpha1.ClusterStatus, message string, generation int64) v1alpha1.ClusterStatus {
	next := failPhase(status, message, generation)
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonRestoreManual, message, generation)
	return next
}

func abortCleanup(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) error {
	message, done, err := cleanUp(ctx, env, cluster)
	if err != nil {
		return err
	}
	if !done {
		return writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
			*s = withMessage(*s, message)
		})
	}
	if err := writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
		*s = abortUpgrade(*s, message, cluster.Generation)
	}); err != nil {
		return err
	}
	return endAbort(ctx, env.Client, cluster)
}

func cleanUp(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (string, bool, error) {
	upgrade := cluster.Status.Upgrade
	if upgrade == nil {
		return "", true, nil
	}
	if err := restoreOperatorImage(ctx, env.Client, cluster.Status.Version); err != nil {
		return "", false, err
	}
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return "", false, err
	}
	if err := appendStep(ctx, env.Client, *upgrade, hosts, nodes, v1alpha1.StepCleanup); err != nil {
		return "", false, err
	}
	upgrades, err := listNodeUpgrades(ctx, env.Client, upgrade.To)
	if err != nil {
		return "", false, err
	}
	summary := summarizeStep(upgrades, hostNames(hosts), v1alpha1.StepCleanup)
	if len(summary.Waiting) > 0 {
		return fmt.Sprintf("abort: cleanup %d/%d nodes, waiting for %s", summary.Succeeded+len(summary.Failed), summary.Total, strings.Join(summary.Waiting, ", ")), false, nil
	}
	if err := deleteNodeUpgrades(ctx, env.Client, upgrade.To); err != nil {
		return "", false, err
	}
	if len(summary.Failed) > 0 {
		return "cleanup failed on " + strings.Join(summary.Failed, "; "), true, nil
	}
	return "", true, nil
}

func restoreOperatorImage(ctx context.Context, c client.Client, version string) error {
	from, err := optionalRelease(ctx, c, version)
	if err != nil {
		return err
	}
	if from == nil {
		return fmt.Errorf("Release/%s does not exist", version)
	}
	return setOperatorImage(ctx, c, from.Spec.Image)
}

func endAbort(ctx context.Context, c client.Client, cluster v1alpha1.Cluster) error {
	ended := cluster.DeepCopy()
	ended.Spec.DesiredVersion = cluster.Status.Version
	ended.Spec.Upgrade.Action = ""
	return c.Patch(ctx, ended, client.MergeFrom(&cluster))
}

func abortHandoff(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) error {
	from, err := optionalRelease(ctx, env.Client, cluster.Status.Version)
	if err != nil {
		return err
	}
	if from == nil {
		return fmt.Errorf("Release/%s does not exist", cluster.Status.Version)
	}
	if err := setOperatorImage(ctx, env.Client, from.Spec.Image); err != nil {
		return err
	}
	return writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
		*s = withMessage(*s, "abort: operator switching back to "+from.Spec.Image)
	})
}

func restoreControlPlane(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) error {
	upgrade := *cluster.Status.Upgrade
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return err
	}
	controllers := hostsWithRole(hosts, v1alpha1.RoleControlPlane)
	node, path, ok := parseBackupLocation(upgrade.Backup)
	if len(controllers) != 1 || !ok {
		return answerManualRestore(ctx, env, cluster, fmt.Sprintf("abort in ControlPlane needs a manual restore of %s on %d controllers: follow %s", upgrade.Backup, len(controllers), restoreRunbook))
	}
	if len(nodes) != 1 {
		return answerManualRestore(ctx, env, cluster, fmt.Sprintf("abort in ControlPlane needs a manual restore of %s: the cluster has %d nodes, and the automatic restore runs only on a single node: follow %s", upgrade.Backup, len(nodes), restoreRunbook))
	}
	if node != controllers[0].Name {
		problem := fmt.Sprintf("the backup node %s is not the controller %s", node, controllers[0].Name)
		return answerManualRestore(ctx, env, cluster, fmt.Sprintf("abort in ControlPlane needs a manual restore of %s: %s: follow %s", upgrade.Backup, problem, restoreRunbook))
	}
	want, problem := nodeUpgradeFor(upgrade, hostNamed(hosts, node), hosts, nodes, []string{v1alpha1.StepRestore})
	if problem != "" {
		return answerManualRestore(ctx, env, cluster, fmt.Sprintf("abort in ControlPlane needs a manual restore of %s: %s: follow %s", upgrade.Backup, problem, restoreRunbook))
	}
	want.Spec.Backup = path
	if err := applyNodeUpgrade(ctx, env.Client, want); err != nil {
		return err
	}
	if err := restoreOperatorImage(ctx, env.Client, cluster.Status.Version); err != nil {
		return err
	}
	return writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
		*s = restoring(*s, "restoring "+upgrade.Backup, cluster.Generation)
	})
}

func answerManualRestore(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster, message string) error {
	return answerAction(ctx, env.Client, cluster, func(s v1alpha1.ClusterStatus) v1alpha1.ClusterStatus {
		return restoreManual(s, message, cluster.Generation)
	})
}

func awaitRestore(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) error {
	upgrade := *cluster.Status.Upgrade
	node, _, _ := parseBackupLocation(upgrade.Backup)
	current, err := readNodeUpgrade(ctx, env.Client, v1alpha1.NodeUpgradeName(upgrade.To, node))
	if err != nil {
		return err
	}
	step := currentStep(current, v1alpha1.StepRestore)
	if step.State == v1alpha1.StepFailed {
		message := fmt.Sprintf("restore on %s failed: %s: follow %s", node, step.Message, restoreRunbook)
		return answerAction(ctx, env.Client, cluster, func(s v1alpha1.ClusterStatus) v1alpha1.ClusterStatus {
			return restoreManual(s, message, cluster.Generation)
		})
	}
	return writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
		*s = withMessage(*s, fmt.Sprintf("abort: waiting for the restore of %s (%s)", upgrade.Backup, step.State))
	})
}
