package operator

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	kubevirtNamespace   = "kubevirt"
	keyEvictionStrategy = "virt.eviction-strategy"
)

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
	tuning, err := parseTuning(in.Settings)
	if err != nil {
		return Rendered{}, err
	}
	smbios, err := parseSMBIOS(in.Settings)
	if err != nil {
		return Rendered{}, err
	}
	migration, err := parseMigrationTuning(in.Settings)
	if err != nil {
		return Rendered{}, err
	}
	eviction, err := parseEviction(in.Settings)
	if err != nil {
		return Rendered{}, err
	}
	policies, absent, err := compressionPolicy(in.Settings)
	if err != nil {
		return Rendered{}, err
	}
	objects := append([]*unstructured.Unstructured{kubeVirt(emulation, tuning, migration, smbios, eviction), cdi()}, policies...)
	probes := []Probe{
		{GVK: kubeVirtGVK, Key: client.ObjectKey{Namespace: kubevirtNamespace, Name: "kubevirt"}, Gate: release.PhaseGate("Deployed")},
		{GVK: cdiGVK, Key: client.ObjectKey{Name: "cdi"}, Gate: release.PhaseGate("Deployed")},
	}
	return Rendered{Objects: objects, Absent: absent, Probes: probes}, nil
}

func boolSetting(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func kubeVirt(emulation bool, tuning kubeVirtTuning, migration migrationTuning, smbios map[string]any, eviction string) *unstructured.Unstructured {
	return object("kubevirt.io/v1", "KubeVirt", kubevirtNamespace, "kubevirt", map[string]any{
		"imagePullPolicy": "IfNotPresent",
		"configuration": withSMBIOS(map[string]any{
			"commonInstancetypesDeployment": map[string]any{"enabled": true},
			"vmStateStorageClass":           "filesystem",
			"evictionStrategy":              eviction,
			"supportContainerResources":     supportContainerResources(),
			"migrations":                    migrationSettings(migration, tuning.ParallelMigrations),
			"developerConfiguration": map[string]any{
				"useEmulation":       emulation,
				"featureGates":       []any{"CPUManager", "HostDevices"},
				"cpuAllocationRatio": tuning.CPUAllocationRatio,
				"memoryOvercommit":   tuning.MemoryOvercommit,
			},
		}, smbios),
		"workloadUpdateStrategy": map[string]any{"workloadUpdateMethods": []any{"LiveMigrate", "Evict"}},
	})
}

func parseEviction(values map[string]string) (string, error) {
	value := settings.OrDefault(values[keyEvictionStrategy], keyEvictionStrategy)
	definition, _ := settings.Lookup(keyEvictionStrategy)
	if err := definition.Validate(value); err != nil {
		return "", fmt.Errorf("%s: %w", keyEvictionStrategy, err)
	}
	return value, nil
}

func supportContainerResources() []any {
	return []any{map[string]any{
		"type":      "guest-console-log",
		"resources": map[string]any{"limits": map[string]any{"cpu": "100m", "memory": "120M"}},
	}}
}

func withSMBIOS(configuration, smbios map[string]any) map[string]any {
	if len(smbios) > 0 {
		configuration["smbios"] = smbios
	}
	return configuration
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
