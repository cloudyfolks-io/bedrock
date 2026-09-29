package operator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	fabricNamespace       = "kube-system"
	ovsDaemonSet          = "ovs-ovn"
	ovsPodLabel           = "app.kubernetes.io/name"
	ovsPodLabelValue      = "kube-ovn-ovs"
	revisionHashLabel     = "controller-revision-hash"
	outdatedLauncherLabel = "kubevirt.io/outdatedLauncherImage"

	ovsDelete = "delete"
	ovsWait   = "wait"
	ovsDone   = "done"
)

func components(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	vars, err := release.Vars(ctx, env.Client, cluster.Spec.API.VIP)
	if err != nil {
		return phaseResult{}, err
	}
	if err := writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
		*s = resetComponents(*s)
	}); err != nil {
		return phaseResult{}, err
	}
	report := func(group release.Group, groupErr error) {
		if err := writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
			*s = reportComponent(*s, componentStatus(env.Bundle, group, groupErr))
		}); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "write component status", "component", group.Name)
		}
	}
	err = release.InstallGroups(ctx, env.Client, env.Bundle, vars, env.Gates, env.PollInterval, env.GroupTimeout, report, authnBootstrapHook(env.Client), componentHook(env, cluster))
	switch {
	case err != nil && ctx.Err() != nil:
		return phaseResult{}, err
	case err != nil:
		return phaseResult{Failure: "components: " + err.Error()}, nil
	}
	return phaseResult{Done: true}, nil
}

func resetComponents(status v1alpha1.ClusterStatus) v1alpha1.ClusterStatus {
	if status.Phase != v1alpha1.PhaseComponents {
		return status
	}
	next := *status.DeepCopy()
	next.Components = nil
	return next
}

func reportComponent(status v1alpha1.ClusterStatus, component v1alpha1.ComponentStatus) v1alpha1.ClusterStatus {
	if status.Phase != v1alpha1.PhaseComponents {
		return status
	}
	next := *status.DeepCopy()
	next.Components = append(next.Components, component)
	state := "ready"
	if !component.Available {
		state = "failed"
	}
	return withMessage(next, fmt.Sprintf("components: %s %s", component.Name, state))
}

func componentHook(env upgradeEnv, cluster v1alpha1.Cluster) func(context.Context, release.Group) error {
	return func(ctx context.Context, group release.Group) error {
		switch group.Name {
		case "fabric":
			return cycleOVS(ctx, env.Client, env.PollInterval)
		case "rook-csi":
			return waitForAddon(ctx, env, cluster, storageAddon)
		case "kubevirt":
			if err := waitForAddon(ctx, env, cluster, virtualizationAddon); err != nil {
				return err
			}
			return waitForLaunchers(ctx, env.Client, env.PollInterval)
		case "traefik":
			return waitForAddon(ctx, env, cluster, platformAddon)
		}
		return nil
	}
}

func waitUntil(ctx context.Context, interval time.Duration, check func(context.Context) (bool, string, error)) error {
	var status string
	err := wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		done, current, err := check(ctx)
		if ctx.Err() == nil {
			status = current
		}
		return done, err
	})
	if err != nil && ctx.Err() != nil && status != "" {
		return fmt.Errorf("%s: %w", status, err)
	}
	return err
}

func cycleOVS(ctx context.Context, c client.Client, interval time.Duration) error {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(fabricNamespace), client.MatchingLabels{ovsPodLabel: ovsPodLabelValue}); err != nil {
		return err
	}
	for _, node := range podNodes(pods.Items) {
		if err := waitUntil(ctx, interval, func(ctx context.Context) (bool, string, error) {
			return cycleOVSOn(ctx, c, node)
		}); err != nil {
			return err
		}
	}
	return nil
}

func podNodes(pods []corev1.Pod) []string {
	var nodes []string
	for _, pod := range pods {
		if pod.Spec.NodeName != "" && !slices.Contains(nodes, pod.Spec.NodeName) {
			nodes = append(nodes, pod.Spec.NodeName)
		}
	}
	slices.Sort(nodes)
	return nodes
}

func cycleOVSOn(ctx context.Context, c client.Client, node string) (bool, string, error) {
	prefix := "ovs-ovn on " + node
	revision, found, err := ovsRevision(ctx, c)
	if err != nil {
		return false, prefix + ": " + err.Error(), nil
	}
	if !found {
		return true, "", nil
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(fabricNamespace), client.MatchingLabels{ovsPodLabel: ovsPodLabelValue}); err != nil {
		return false, prefix + ": " + err.Error(), nil
	}
	pod := livePodOn(pods.Items, node)
	switch ovsAction(pod, revision) {
	case ovsDone:
		return true, "", nil
	case ovsDelete:
		if err := c.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return false, prefix + ": " + err.Error(), nil
		}
		return false, prefix + ": replacing " + pod.Name, nil
	}
	return false, prefix + ": waiting for the new pod", nil
}

func ovsRevision(ctx context.Context, c client.Client) (string, bool, error) {
	var daemonSet appsv1.DaemonSet
	err := c.Get(ctx, client.ObjectKey{Namespace: fabricNamespace, Name: ovsDaemonSet}, &daemonSet)
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var revisions appsv1.ControllerRevisionList
	if err := c.List(ctx, &revisions, client.InNamespace(fabricNamespace), client.MatchingLabels{ovsPodLabel: ovsPodLabelValue}); err != nil {
		return "", false, err
	}
	latest, ok := latestRevision(revisions.Items, daemonSet.UID)
	if !ok {
		return "", false, fmt.Errorf("daemonset %s/%s has no controller revision", fabricNamespace, ovsDaemonSet)
	}
	return latest.Labels[revisionHashLabel], true, nil
}

func latestRevision(revisions []appsv1.ControllerRevision, owner types.UID) (appsv1.ControllerRevision, bool) {
	var latest appsv1.ControllerRevision
	found := false
	for _, revision := range revisions {
		owned := slices.ContainsFunc(revision.OwnerReferences, func(ref metav1.OwnerReference) bool { return ref.UID == owner })
		if owned && (!found || revision.Revision > latest.Revision) {
			latest, found = revision, true
		}
	}
	return latest, found
}

func livePodOn(pods []corev1.Pod, node string) *corev1.Pod {
	for i := range pods {
		if pods[i].Spec.NodeName == node && pods[i].DeletionTimestamp == nil {
			return &pods[i]
		}
	}
	return nil
}

func ovsAction(pod *corev1.Pod, revision string) string {
	switch {
	case pod == nil || pod.DeletionTimestamp != nil:
		return ovsWait
	case pod.Labels[revisionHashLabel] != revision:
		return ovsDelete
	case podReady(*pod):
		return ovsDone
	}
	return ovsWait
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func waitForAddon(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster, addon Addon) error {
	reconciler := &AddonReconciler{Client: env.Client, Bundle: env.Bundle}
	err := waitUntil(ctx, env.PollInterval, func(ctx context.Context) (bool, string, error) {
		input, err := reconciler.input(ctx, cluster)
		if err != nil {
			return false, err.Error(), nil
		}
		rendered, err := addon.Render(input)
		if err != nil {
			return false, "", err
		}
		if rendered.SkipReason != "" {
			return true, "", nil
		}
		condition := reconciler.reconcileAddon(ctx, addon, input)
		if condition.Reason == "Failed" {
			return false, "", errors.New(condition.Message)
		}
		return condition.Status == metav1.ConditionTrue, condition.Message, nil
	})
	if err != nil {
		return fmt.Errorf("%s addon: %w", addon.Name, err)
	}
	return nil
}

func waitForLaunchers(ctx context.Context, c client.Client, interval time.Duration) error {
	return waitUntil(ctx, interval, func(ctx context.Context) (bool, string, error) {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.HasLabels{outdatedLauncherLabel}); err != nil {
			return false, err.Error(), nil
		}
		names := make([]string, 0, len(pods.Items))
		for _, pod := range pods.Items {
			names = append(names, pod.Namespace+"/"+pod.Name)
		}
		return len(names) == 0, "outdated virt-launcher pods: " + strings.Join(names, ", "), nil
	})
}
