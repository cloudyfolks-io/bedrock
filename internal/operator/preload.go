package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const (
	operatorDeployment = "bedrock-operator"
	operatorContainer  = "operator"
)

func preload(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	upgrade := *cluster.Status.Upgrade
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return phaseResult{}, err
	}
	for _, host := range hosts {
		want, problem := nodeUpgradeFor(upgrade, host, hosts, nodes, []string{v1alpha1.StepPreload})
		if problem != "" {
			return phaseResult{Message: "preload: " + problem}, nil
		}
		if err := applyNodeUpgrade(ctx, env.Client, want); err != nil {
			return phaseResult{}, err
		}
	}
	upgrades, err := listNodeUpgrades(ctx, env.Client, upgrade.To)
	if err != nil {
		return phaseResult{}, err
	}
	return stepResult("preload", summarizeStep(upgrades, hostNames(hosts), v1alpha1.StepPreload)), nil
}

func preloadThenSwitch(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	result, err := preload(ctx, env, cluster)
	if err != nil || !result.Done {
		return result, err
	}
	to := cluster.Status.Upgrade.To
	target, err := optionalRelease(ctx, env.Client, to)
	if err != nil {
		return phaseResult{}, err
	}
	if target == nil {
		return phaseResult{Failure: fmt.Sprintf("switch: Release/%s does not exist", to)}, nil
	}
	if err := setOperatorImage(ctx, env.Client, target.Spec.Image); err != nil {
		return phaseResult{}, err
	}
	return phaseResult{Message: "preload done, operator switching to " + target.Spec.Image}, nil
}

func hostsAndNodes(ctx context.Context, c client.Client) ([]v1alpha1.Host, []corev1.Node, error) {
	var hosts v1alpha1.HostList
	if err := c.List(ctx, &hosts); err != nil {
		return nil, nil, err
	}
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return nil, nil, err
	}
	return slices.SortedFunc(slices.Values(hosts.Items), compareHosts), nodes.Items, nil
}

func hostNames(hosts []v1alpha1.Host) []string {
	names := make([]string, 0, len(hosts))
	for _, host := range hosts {
		names = append(names, host.Name)
	}
	return names
}

func hostArchitecture(host v1alpha1.Host, nodes []corev1.Node) (string, bool) {
	if node, ok := findNode(nodes, host.Name); ok {
		return node.Status.NodeInfo.Architecture, true
	}
	arches := nodeArchitectures(nodes)
	if len(arches) == 1 {
		return arches[0], true
	}
	return "", false
}

func depotURL(hosts []v1alpha1.Host, version, arch string) (string, bool) {
	for _, host := range hosts {
		if host.Status.Depot == nil {
			continue
		}
		for _, bundle := range host.Status.Depot.Bundles {
			if bundle.Version == version && bundle.Arch == arch {
				return depot.BundleURL(host.Status.Depot.URL, version, arch), true
			}
		}
	}
	return "", false
}

func nodeUpgradeFor(upgrade v1alpha1.UpgradeStatus, host v1alpha1.Host, hosts []v1alpha1.Host, nodes []corev1.Node, steps []string) (v1alpha1.NodeUpgrade, string) {
	arch, ok := hostArchitecture(host, nodes)
	if !ok {
		return v1alpha1.NodeUpgrade{}, fmt.Sprintf("host %s has no Node and the cluster has several architectures", host.Name)
	}
	url, ok := depotURL(hosts, upgrade.To, arch)
	if !ok {
		return v1alpha1.NodeUpgrade{}, fmt.Sprintf("no host serves %s for %s", upgrade.To, arch)
	}
	return v1alpha1.NodeUpgrade{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName(upgrade.To, host.Name)},
		Spec:       v1alpha1.NodeUpgradeSpec{Node: host.Name, Version: upgrade.To, From: upgrade.From, Depot: url, Attempt: upgrade.Attempt, Steps: steps},
	}, ""
}

func setOperatorImage(ctx context.Context, c client.Client, image string) error {
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: operatorDeployment}, &deployment); err != nil {
		return err
	}
	containers := deployment.Spec.Template.Spec.Containers
	index := slices.IndexFunc(containers, func(container corev1.Container) bool { return container.Name == operatorContainer })
	if index < 0 {
		return fmt.Errorf("deployment %s/%s has no container %s", release.SystemNamespace, operatorDeployment, operatorContainer)
	}
	if containers[index].Image == image {
		return nil
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": fmt.Sprintf("/spec/template/spec/containers/%d/name", index), "value": operatorContainer},
		{"op": "replace", "path": fmt.Sprintf("/spec/template/spec/containers/%d/image", index), "value": image},
	})
	if err != nil {
		return err
	}
	return c.Patch(ctx, &deployment, client.RawPatch(types.JSONPatchType, patch))
}
