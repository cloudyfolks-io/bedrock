package operator

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const kubevirtNamespace = "kubevirt"

var virtualizationAddon = Addon{Name: "virtualization", Condition: v1alpha1.ConditionVirtualizationReady, Render: RenderVirtualization}

var (
	kubeVirtGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "KubeVirt"}
	cdiGVK      = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "CDI"}
)

func RenderVirtualization(in AddonInput) (Rendered, error) {
	emulation, err := boolSetting(in.Settings["virt.emulation"])
	if err != nil {
		return Rendered{}, fmt.Errorf("virt.emulation: %w", err)
	}
	objects := []*unstructured.Unstructured{kubeVirt(emulation), cdi()}
	probes := []Probe{
		{GVK: kubeVirtGVK, Key: client.ObjectKey{Namespace: kubevirtNamespace, Name: "kubevirt"}, Gate: release.PhaseGate("Deployed")},
		{GVK: cdiGVK, Key: client.ObjectKey{Name: "cdi"}, Gate: release.PhaseGate("Deployed")},
	}
	return Rendered{Objects: objects, Probes: probes}, nil
}

func boolSetting(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func kubeVirt(emulation bool) *unstructured.Unstructured {
	return object("kubevirt.io/v1", "KubeVirt", kubevirtNamespace, "kubevirt", map[string]any{
		"imagePullPolicy":        "IfNotPresent",
		"configuration":          map[string]any{"developerConfiguration": map[string]any{"useEmulation": emulation}},
		"workloadUpdateStrategy": map[string]any{"workloadUpdateMethods": []any{"LiveMigrate", "Evict"}},
	})
}

func cdi() *unstructured.Unstructured {
	return object("cdi.kubevirt.io/v1beta1", "CDI", "", "cdi", map[string]any{
		"imagePullPolicy": "IfNotPresent",
		"config":          map[string]any{"featureGates": []any{"HonorWaitForFirstConsumer"}},
		"infra":           linuxNodeSelector(),
		"workload":        linuxNodeSelector(),
	})
}

func linuxNodeSelector() map[string]any {
	return map[string]any{"nodeSelector": map[string]any{"kubernetes.io/os": "linux"}}
}
