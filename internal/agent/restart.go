package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	cephDaemonLabel = "ceph_daemon_type"
	onDeleteUpdates = "OnDelete"
	systemNamespace = "kube-system"
	workloadTimeout = "--request-timeout=30s"
	restartAttempts = 3
)

var dataPlaneWorkloads = []string{"ovs-ovn", "ovs-ovn-dpdk", "ovn-central", "kube-vip"}

type workload struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		UpdateStrategy struct {
			Type string `json:"type"`
		} `json:"updateStrategy"`
		Template struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"template"`
	} `json:"spec"`
}

func RestartPlatformWorkloads(ctx context.Context, deps Deps) (int, error) {
	if err := awaitAPI(ctx, deps); err != nil {
		return 0, err
	}
	restartCtx, cancel := context.WithTimeout(ctx, deps.K0sTimeout)
	defer cancel()
	namespaces, err := platformNamespaces(restartCtx, deps)
	if err != nil {
		return 0, err
	}
	workloads, err := listWorkloads(restartCtx, deps)
	if err != nil {
		return 0, err
	}
	var failures []error
	targets := restartOrder(restartable(workloads, namespaces))
	for _, target := range targets {
		if err := retryCall(restartCtx, restartAttempts, deps.K0sPoll, func() error { return restartWorkload(restartCtx, deps, target) }); err != nil {
			failures = append(failures, fmt.Errorf("restart %s %s/%s: %w", target.Kind, target.Metadata.Namespace, target.Metadata.Name, err))
		}
	}
	return len(targets) - len(failures), errors.Join(failures...)
}

func platformNamespaces(ctx context.Context, deps Deps) ([]string, error) {
	out, err := adminKubectl(ctx, deps, "get", "namespaces", "--selector", release.ComponentLabel, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, fmt.Errorf("platform namespaces: %w", err)
	}
	return append(strings.Fields(out), systemNamespace), nil
}

func listWorkloads(ctx context.Context, deps Deps) ([]workload, error) {
	out, err := adminKubectl(ctx, deps, "get", "deployments,daemonsets,statefulsets", "--all-namespaces", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("workload list: %w", err)
	}
	var list struct {
		Items []workload `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("workload list: %w", err)
	}
	return list.Items, nil
}

func restartable(workloads []workload, namespaces []string) []workload {
	var targets []workload
	for _, candidate := range workloads {
		if slices.Contains(namespaces, candidate.Metadata.Namespace) && !dataPlane(candidate) {
			targets = append(targets, candidate)
		}
	}
	return targets
}

func dataPlane(candidate workload) bool {
	_, cephDaemon := candidate.Spec.Template.Metadata.Labels[cephDaemonLabel]
	return cephDaemon || candidate.Spec.UpdateStrategy.Type == onDeleteUpdates || slices.Contains(dataPlaneWorkloads, candidate.Metadata.Name)
}

func restartOrder(targets []workload) []workload {
	ordered := slices.Clone(targets)
	slices.SortStableFunc(ordered, func(a, b workload) int { return restartRank(a) - restartRank(b) })
	return ordered
}

func restartRank(target workload) int {
	switch {
	case target.Kind == "DaemonSet" && target.Metadata.Namespace == systemNamespace:
		return 0
	case target.Kind == "DaemonSet":
		return 1
	case target.Kind == "Deployment":
		return 2
	default:
		return 3
	}
}

func restartWorkload(ctx context.Context, deps Deps, target workload) error {
	_, err := adminKubectl(ctx, deps, "--namespace", target.Metadata.Namespace, "rollout", "restart", strings.ToLower(target.Kind)+"/"+target.Metadata.Name)
	return err
}

func retryCall(ctx context.Context, attempts int, pause time.Duration, call func() error) error {
	err := call()
	for attempt := 1; err != nil && attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(attempt) * pause):
		}
		err = call()
	}
	return err
}

func adminKubectl(ctx context.Context, deps Deps, args ...string) (string, error) {
	return deps.Exec.Run(ctx, k0s.DefaultBinary, append([]string{"kubectl", "--kubeconfig", filepath.Join(deps.Root, adminKubeconfig), workloadTimeout}, args...)...)
}

func awaitAPI(ctx context.Context, deps Deps) error {
	observed, err := waitClear(ctx, deps.K0sTimeout, deps.K0sPoll, func(waitCtx context.Context) string {
		return bounded(waitCtx, deps.ProbeTimeout, func(probeCtx context.Context) string {
			return apiProblem(probeCtx, deps.Exec, deps.Root, "/readyz")
		})
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("the API did not answer within %s: %s", deps.K0sTimeout, observed)
	}
	return err
}
