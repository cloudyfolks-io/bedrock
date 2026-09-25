package operator

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const keptBackups = 3

func backup(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	upgrade := *cluster.Status.Upgrade
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return phaseResult{}, err
	}
	host, ok := backupHost(hosts)
	if !ok {
		return phaseResult{Failure: "backup: no Host has the control-plane role"}, nil
	}
	want, problem := nodeUpgradeFor(upgrade, host, hosts, nodes, []string{v1alpha1.StepBackup})
	if problem != "" {
		return phaseResult{Message: "backup: " + problem}, nil
	}
	if err := applyNodeUpgrade(ctx, env.Client, want); err != nil {
		return phaseResult{}, err
	}
	current, err := readNodeUpgrade(ctx, env.Client, want.Name)
	if err != nil {
		return phaseResult{}, err
	}
	step := currentStep(current, v1alpha1.StepBackup)
	switch step.State {
	case v1alpha1.StepFailed:
		return phaseResult{Failure: fmt.Sprintf("backup on %s: %s", host.Name, step.Message)}, nil
	case v1alpha1.StepSucceeded:
		return finishBackup(ctx, env.Client, host.Name, step)
	}
	return phaseResult{Message: fmt.Sprintf("backup on %s: %s", host.Name, step.State)}, nil
}

func finishBackup(ctx context.Context, c client.Client, node string, step v1alpha1.NodeUpgradeStepStatus) (phaseResult, error) {
	path, digest, err := parseBackupMessage(step.Message)
	if err != nil {
		return phaseResult{Failure: fmt.Sprintf("backup on %s: %v", node, err)}, nil
	}
	record := v1alpha1.BackupRecord{Time: finishedAt(step), Location: backupLocation(node, path), Digest: digest}
	return phaseResult{Done: true}, writeClusterStatus(ctx, c, func(s *v1alpha1.ClusterStatus) {
		*s = recordBackup(*s, record)
	})
}

func finishedAt(step v1alpha1.NodeUpgradeStepStatus) metav1.Time {
	if step.FinishedAt != nil {
		return *step.FinishedAt
	}
	return metav1.Now()
}

func compareHosts(a, b v1alpha1.Host) int {
	return strings.Compare(a.Name, b.Name)
}

func backupHost(hosts []v1alpha1.Host) (v1alpha1.Host, bool) {
	for _, host := range slices.SortedFunc(slices.Values(hosts), compareHosts) {
		if slices.Contains(host.Spec.Roles, v1alpha1.RoleControlPlane) {
			return host, true
		}
	}
	return v1alpha1.Host{}, false
}

func parseBackupMessage(message string) (string, string, error) {
	fields := strings.Fields(message)
	if len(fields) != 2 || !filepath.IsAbs(fields[0]) || !strings.HasPrefix(fields[1], "sha256:") {
		return "", "", fmt.Errorf("backup message %q is not <absolute path> sha256:<hex>", message)
	}
	return fields[0], fields[1], nil
}

func backupLocation(node, path string) string {
	return "host:" + node + ":" + path
}

func recordBackup(status v1alpha1.ClusterStatus, record v1alpha1.BackupRecord) v1alpha1.ClusterStatus {
	next := *status.DeepCopy()
	if next.Upgrade != nil {
		next.Upgrade.Backup = record.Location
	}
	if slices.ContainsFunc(next.Backups, func(existing v1alpha1.BackupRecord) bool { return existing.Location == record.Location }) {
		return next
	}
	backups := append(next.Backups, record)
	next.Backups = backups[max(0, len(backups)-keptBackups):]
	return next
}
