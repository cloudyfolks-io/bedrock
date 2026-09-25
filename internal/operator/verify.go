package operator

import (
	"context"
	"fmt"
	"net"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

const (
	smokeName         = "bedrock-smoke"
	smokeImage        = "quay.io/kubevirt/cirros-container-disk-demo:v1.9.0"
	smokeStorageClass = "block"
	smokeTimeout      = 15 * time.Minute
)

func verify(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	upgrade := *cluster.Status.Upgrade
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return phaseResult{}, err
	}
	upgrades, err := listNodeUpgrades(ctx, env.Client, upgrade.To)
	if err != nil {
		return phaseResult{}, err
	}
	if !pruneStarted(upgrades) {
		return smokeTest(ctx, env, cluster, hosts, nodes)
	}
	if err := deleteSmokeVM(ctx, env.Client); err != nil {
		return phaseResult{}, err
	}
	if err := appendStep(ctx, env.Client, upgrade, hosts, nodes, v1alpha1.StepPrune); err != nil {
		return phaseResult{}, err
	}
	result := stepResult("verify: prune", summarizeStep(upgrades, hostNames(hosts), v1alpha1.StepPrune))
	if !result.Done {
		return result, nil
	}
	return result, deleteNodeUpgrades(ctx, env.Client, upgrade.To)
}

func pruneStarted(upgrades []v1alpha1.NodeUpgrade) bool {
	return slices.ContainsFunc(upgrades, func(upgrade v1alpha1.NodeUpgrade) bool {
		return slices.Contains(upgrade.Spec.Steps, v1alpha1.StepPrune)
	})
}

func smokeTest(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster, hosts []v1alpha1.Host, nodes []corev1.Node) (phaseResult, error) {
	class, err := storageClassFor(ctx, env.Client, smokeStorageClass)
	if err != nil {
		return phaseResult{}, err
	}
	vm := smokeVM()
	if class != "" {
		vm = withScratchDisk(vm, class)
	}
	if len(nodes) > 1 {
		vm = awayFromOperator(vm)
	}
	result, err := runSmoke(ctx, env, vm)
	switch {
	case err != nil || result.Failure != "":
		return result, err
	case !result.Done && time.Since(cluster.Status.Upgrade.PhaseStartedAt.Time) > smokeTimeout:
		return phaseResult{Failure: fmt.Sprintf("verify: smoke VM not reached within %s: %s", smokeTimeout, result.Message)}, nil
	case !result.Done:
		return phaseResult{Message: "verify: " + result.Message}, nil
	}
	if err := appendStep(ctx, env.Client, *cluster.Status.Upgrade, hosts, nodes, v1alpha1.StepPrune); err != nil {
		return phaseResult{}, err
	}
	return phaseResult{Message: "verify: " + result.Message + diskNote(class)}, nil
}

func diskNote(class string) string {
	if class == "" {
		return ", disk skipped: no StorageClass " + smokeStorageClass
	}
	return " with a 1Gi disk"
}

func storageClassFor(ctx context.Context, c client.Client, name string) (string, error) {
	var class storagev1.StorageClass
	err := c.Get(ctx, client.ObjectKey{Name: name}, &class)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

func runSmoke(ctx context.Context, env upgradeEnv, vm *unstructured.Unstructured) (phaseResult, error) {
	if err := ssa.Apply(ctx, env.Client, vm, v1alpha1.OperatorFieldManager); err != nil {
		return phaseResult{}, err
	}
	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(vmiGVK)
	err := env.Client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: smokeName}, instance)
	if apierrors.IsNotFound(err) {
		return phaseResult{Message: "starting the smoke VM"}, nil
	}
	if err != nil {
		return phaseResult{}, err
	}
	phase, _, _ := unstructured.NestedString(instance.Object, "status", "phase")
	address, hasAddress := vmiAddress(*instance)
	switch {
	case phase == "Failed":
		return phaseResult{Failure: "verify: smoke VM failed"}, nil
	case phase != "Running":
		return phaseResult{Message: "smoke VM is " + stateName(phase)}, nil
	case !hasAddress:
		return phaseResult{Message: "smoke VM has no IP address yet"}, nil
	}
	if err := env.Dial(ctx, address); err != nil {
		return phaseResult{Message: fmt.Sprintf("smoke VM %s: %v", address, err)}, nil
	}
	return phaseResult{Done: true, Message: "smoke VM reached " + address}, nil
}

func vmiAddress(instance unstructured.Unstructured) (string, bool) {
	interfaces, _, _ := unstructured.NestedSlice(instance.Object, "status", "interfaces")
	for _, item := range interfaces {
		fields, _ := item.(map[string]any)
		if ip, _ := fields["ipAddress"].(string); ip != "" {
			return net.JoinHostPort(ip, "22"), true
		}
	}
	return "", false
}

func dialTCP(ctx context.Context, address string) error {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

func smokeVM() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata":   map[string]any{"name": smokeName, "namespace": release.SystemNamespace},
		"spec": map[string]any{
			"runStrategy": "Always",
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": smokeName}},
				"spec": map[string]any{
					"domain": map[string]any{
						"devices": map[string]any{
							"disks":      []any{map[string]any{"name": "containerdisk", "disk": map[string]any{"bus": "virtio"}}},
							"interfaces": []any{map[string]any{"name": "default", "masquerade": map[string]any{}}},
						},
						"resources": map[string]any{"requests": map[string]any{"memory": "128Mi"}},
					},
					"networks": []any{map[string]any{"name": "default", "pod": map[string]any{}}},
					"volumes":  []any{map[string]any{"name": "containerdisk", "containerDisk": map[string]any{"image": smokeImage}}},
				},
			},
		},
	}}
}

func withScratchDisk(vm *unstructured.Unstructured, class string) *unstructured.Unstructured {
	next := vm.DeepCopy()
	scratch := smokeName + "-scratch"
	disks, _, _ := unstructured.NestedSlice(next.Object, "spec", "template", "spec", "domain", "devices", "disks")
	volumes, _, _ := unstructured.NestedSlice(next.Object, "spec", "template", "spec", "volumes")
	_ = unstructured.SetNestedSlice(next.Object, append(disks, map[string]any{"name": "scratch", "disk": map[string]any{"bus": "virtio"}}), "spec", "template", "spec", "domain", "devices", "disks")
	_ = unstructured.SetNestedSlice(next.Object, append(volumes, map[string]any{"name": "scratch", "dataVolume": map[string]any{"name": scratch}}), "spec", "template", "spec", "volumes")
	_ = unstructured.SetNestedSlice(next.Object, []any{map[string]any{
		"metadata": map[string]any{"name": scratch},
		"spec": map[string]any{
			"source":  map[string]any{"blank": map[string]any{}},
			"storage": map[string]any{"storageClassName": class, "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}},
		},
	}}, "spec", "dataVolumeTemplates")
	return next
}

func awayFromOperator(vm *unstructured.Unstructured) *unstructured.Unstructured {
	next := vm.DeepCopy()
	_ = unstructured.SetNestedMap(next.Object, map[string]any{"podAntiAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
		"labelSelector": map[string]any{"matchLabels": map[string]any{"app": operatorDeployment}},
		"namespaces":    []any{release.SystemNamespace},
		"topologyKey":   "kubernetes.io/hostname",
	}}}}, "spec", "template", "spec", "affinity")
	return next
}

func deleteSmokeVM(ctx context.Context, c client.Client) error {
	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(vmGVK)
	vm.SetNamespace(release.SystemNamespace)
	vm.SetName(smokeName)
	err := c.Delete(ctx, vm)
	if meta.IsNoMatchError(err) {
		return nil
	}
	return client.IgnoreNotFound(err)
}

func appendStep(ctx context.Context, c client.Client, upgrade v1alpha1.UpgradeStatus, hosts []v1alpha1.Host, nodes []corev1.Node, step string) error {
	for _, host := range hosts {
		want, problem := nodeUpgradeFor(upgrade, host, hosts, nodes, []string{step})
		if problem != "" {
			return fmt.Errorf("%s: %s", step, problem)
		}
		if err := applyNodeUpgrade(ctx, c, want); err != nil {
			return err
		}
	}
	return nil
}

func deleteNodeUpgrades(ctx context.Context, c client.Client, version string) error {
	upgrades, err := listNodeUpgrades(ctx, c, version)
	if err != nil {
		return err
	}
	for _, upgrade := range upgrades {
		if err := c.Delete(ctx, &upgrade); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}
