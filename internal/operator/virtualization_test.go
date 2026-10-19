package operator

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderVirtualizationDefaults(t *testing.T) {
	out, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "false"}})
	if err != nil {
		t.Fatal(err)
	}
	kv := findObject(out.Objects, "KubeVirt", "kubevirt")
	if kv == nil || kv.GetNamespace() != "kubevirt" || kv.GetAPIVersion() != "kubevirt.io/v1" {
		t.Fatalf("objects %+v", out.Objects)
	}
	emulation, _, _ := unstructured.NestedBool(kv.Object, "spec", "configuration", "developerConfiguration", "useEmulation")
	methods, _, _ := unstructured.NestedStringSlice(kv.Object, "spec", "workloadUpdateStrategy", "workloadUpdateMethods")
	pull, _, _ := unstructured.NestedString(kv.Object, "spec", "imagePullPolicy")
	if emulation || !slices.Equal(methods, []string{"LiveMigrate", "Evict"}) || pull != "IfNotPresent" {
		t.Fatalf("kubevirt spec %+v", kv.Object["spec"])
	}
	cdi := findObject(out.Objects, "CDI", "cdi")
	if cdi == nil || cdi.GetNamespace() != "" || cdi.GetAPIVersion() != "cdi.kubevirt.io/v1beta1" {
		t.Fatalf("objects %+v", out.Objects)
	}
	gates, _, _ := unstructured.NestedStringSlice(cdi.Object, "spec", "config", "featureGates")
	selector, _, _ := unstructured.NestedString(cdi.Object, "spec", "workload", "nodeSelector", "kubernetes.io/os")
	if len(gates) != 1 || gates[0] != "HonorWaitForFirstConsumer" || selector != "linux" {
		t.Fatalf("cdi spec %+v", cdi.Object["spec"])
	}
	if len(out.Probes) != 2 || out.Probes[0].GVK.Kind != "KubeVirt" || out.Probes[0].Key.Namespace != "kubevirt" || out.Probes[1].GVK.Kind != "CDI" || out.Probes[1].Key.Name != "cdi" {
		t.Fatalf("probes %+v", out.Probes)
	}
	deployed := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "kubevirt"}, "status": map[string]any{"phase": "Deployed"}}}
	if r := out.Probes[0].Gate(deployed); !r.Ready {
		t.Fatalf("readiness %+v", r)
	}
}

func TestRenderVirtualizationEmulation(t *testing.T) {
	out, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	kv := findObject(out.Objects, "KubeVirt", "kubevirt")
	emulation, _, _ := unstructured.NestedBool(kv.Object, "spec", "configuration", "developerConfiguration", "useEmulation")
	if !emulation {
		t.Fatalf("kubevirt spec %+v", kv.Object["spec"])
	}
}

func TestRenderVirtualizationRejectsBadEmulation(t *testing.T) {
	if _, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "maybe"}}); err == nil {
		t.Fatal("maybe must fail")
	}
	if _, err := RenderVirtualization(AddonInput{Settings: map[string]string{}}); err != nil {
		t.Fatalf("empty value must default to false: %v", err)
	}
}

func TestParseTuning(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		want   kubeVirtTuning
	}{
		{"defaults", map[string]string{}, kubeVirtTuning{4, 100, 2}},
		{"explicit", map[string]string{"overcommit.cpu": "16", "overcommit.memory": "1.5", "migration.parallel": "5"}, kubeVirtTuning{16, 150, 5}},
		{"quarter", map[string]string{"overcommit.memory": "1.25"}, kubeVirtTuning{4, 125, 2}},
		{"half", map[string]string{"overcommit.memory": "0.5"}, kubeVirtTuning{4, 50, 2}},
	}
	for _, tc := range cases {
		got, err := parseTuning(tc.values)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v want %+v", tc.name, got, err, tc.want)
		}
	}
	bad := []struct{ key, value string }{
		{"overcommit.cpu", "0"}, {"overcommit.cpu", "x"}, {"overcommit.cpu", "1.5"},
		{"overcommit.memory", "0.05"}, {"overcommit.memory", "x"},
		{"migration.parallel", "0"}, {"migration.parallel", "x"},
	}
	for _, tc := range bad {
		_, err := parseTuning(map[string]string{tc.key: tc.value})
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s=%s: error %v must name the key", tc.key, tc.value, err)
		}
	}
}

func TestRenderVirtualizationTuning(t *testing.T) {
	out, err := RenderVirtualization(AddonInput{Settings: map[string]string{
		"virt.emulation": "false", "overcommit.cpu": "8", "overcommit.memory": "1.5", "migration.parallel": "3",
	}})
	if err != nil {
		t.Fatal(err)
	}
	kv := findObject(out.Objects, "KubeVirt", "kubevirt")
	spec := kv.Object["spec"].(map[string]any)
	configuration := spec["configuration"].(map[string]any)
	developer := configuration["developerConfiguration"].(map[string]any)
	gates, _, _ := unstructured.NestedStringSlice(developer, "featureGates")
	if developer["cpuAllocationRatio"] != int64(8) || developer["memoryOvercommit"] != int64(150) || developer["useEmulation"] != false || !slices.Equal(gates, []string{"CPUManager", "HostDevices"}) {
		t.Fatalf("developer configuration %+v", developer)
	}
	if got, _, _ := unstructured.NestedInt64(configuration, "migrations", "parallelMigrationsPerCluster"); got != 3 {
		t.Fatalf("parallel migrations %d", got)
	}
	if got, _, _ := unstructured.NestedBool(configuration, "commonInstancetypesDeployment", "enabled"); !got {
		t.Fatalf("common instance types %+v", configuration)
	}
	if configuration["vmStateStorageClass"] != "filesystem" {
		t.Fatalf("vm state storage class %+v", configuration)
	}
	methods, _, _ := unstructured.NestedStringSlice(spec, "workloadUpdateStrategy", "workloadUpdateMethods")
	if !slices.Equal(methods, []string{"LiveMigrate", "Evict"}) || spec["imagePullPolicy"] != "IfNotPresent" {
		t.Fatalf("spec %+v", spec)
	}
}

func TestRenderVirtualizationDefaultTuning(t *testing.T) {
	out, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "false"}})
	if err != nil {
		t.Fatal(err)
	}
	kv := findObject(out.Objects, "KubeVirt", "kubevirt")
	cpu, _, _ := unstructured.NestedInt64(kv.Object, "spec", "configuration", "developerConfiguration", "cpuAllocationRatio")
	memory, _, _ := unstructured.NestedInt64(kv.Object, "spec", "configuration", "developerConfiguration", "memoryOvercommit")
	parallel, _, _ := unstructured.NestedInt64(kv.Object, "spec", "configuration", "migrations", "parallelMigrationsPerCluster")
	if cpu != 4 || memory != 100 || parallel != 2 {
		t.Fatalf("defaults %d %d %d", cpu, memory, parallel)
	}
}

func TestRenderVirtualizationRejectsBadTuning(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"overcommit.memory", "0.05"}, {"overcommit.cpu", "0"}, {"migration.parallel", "x"}} {
		_, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "false", tc.key: tc.value}})
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s=%s: error %v", tc.key, tc.value, err)
		}
	}
}

func TestRenderVirtualizationSMBIOS(t *testing.T) {
	render := func(values map[string]string) map[string]any {
		values["virt.emulation"] = "false"
		out, err := RenderVirtualization(AddonInput{Settings: values})
		if err != nil {
			t.Fatal(err)
		}
		configuration, _, _ := unstructured.NestedMap(findObject(out.Objects, "KubeVirt", "kubevirt").Object, "spec", "configuration")
		return configuration
	}
	if _, found := render(map[string]string{})["smbios"]; found {
		t.Fatal("smbios must be absent when every Setting is empty")
	}
	all := render(map[string]string{
		"virt.smbios.manufacturer": "Cloudyfolks", "virt.smbios.product": "Bedrock", "virt.smbios.family": "Bedrock",
		"virt.smbios.version": "1.0", "virt.smbios.sku": "ir-central-1",
	})
	want := map[string]any{"manufacturer": "Cloudyfolks", "product": "Bedrock", "family": "Bedrock", "version": "1.0", "sku": "ir-central-1"}
	if !reflect.DeepEqual(all["smbios"], want) {
		t.Fatalf("smbios %+v", all["smbios"])
	}
	partial := render(map[string]string{"virt.smbios.product": "Bedrock"})
	if !reflect.DeepEqual(partial["smbios"], map[string]any{"product": "Bedrock"}) {
		t.Fatalf("partial smbios %+v", partial["smbios"])
	}
}

func TestRenderVirtualizationRejectsBadSMBIOS(t *testing.T) {
	_, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "false", "virt.smbios.sku": "a\nb"}})
	if err == nil || !strings.Contains(err.Error(), "virt.smbios.sku") {
		t.Fatalf("error %v", err)
	}
}

func renderKubeVirt(t *testing.T, settings map[string]string) (*unstructured.Unstructured, Rendered) {
	t.Helper()
	settings["virt.emulation"] = "false"
	out, err := RenderVirtualization(AddonInput{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	return findObject(out.Objects, "KubeVirt", "kubevirt"), out
}

func TestRenderVirtualizationMigrationDefaults(t *testing.T) {
	kv, _ := renderKubeVirt(t, map[string]string{})
	migrations, _, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "migrations")
	want := map[string]any{
		"parallelMigrationsPerCluster":      int64(2),
		"parallelOutboundMigrationsPerNode": int64(4),
		"bandwidthPerMigration":             "1Gi",
		"completionTimeoutPerGiB":           int64(800),
		"progressTimeout":                   int64(150),
	}
	if !reflect.DeepEqual(migrations, want) {
		t.Fatalf("migrations %+v", migrations)
	}
}

func TestRenderVirtualizationMigrationSettings(t *testing.T) {
	kv, _ := renderKubeVirt(t, map[string]string{
		"migration.parallel": "3", "migration.parallel-outbound": "6", "migration.bandwidth": "512Mi",
		"migration.completion-timeout-per-gib": "900", "migration.progress-timeout": "200",
	})
	migrations, _, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "migrations")
	want := map[string]any{
		"parallelMigrationsPerCluster":      int64(3),
		"parallelOutboundMigrationsPerNode": int64(6),
		"bandwidthPerMigration":             "512Mi",
		"completionTimeoutPerGiB":           int64(900),
		"progressTimeout":                   int64(200),
	}
	if !reflect.DeepEqual(migrations, want) {
		t.Fatalf("migrations %+v", migrations)
	}
}

func TestRenderVirtualizationRejectsBadMigrationSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"migration.parallel-outbound", "0"}, {"migration.parallel-outbound", "x"},
		{"migration.bandwidth", "fast"}, {"migration.bandwidth", "0"}, {"migration.bandwidth", "-1Gi"},
		{"migration.completion-timeout-per-gib", "0"}, {"migration.progress-timeout", "x"},
		{"migration.compression", "lz4"},
	} {
		_, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "false", tc.key: tc.value}})
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s=%s: error %v", tc.key, tc.value, err)
		}
	}
}

func TestRenderVirtualizationCompression(t *testing.T) {
	_, off := renderKubeVirt(t, map[string]string{})
	if findObject(off.Objects, "MigrationPolicy", migrationPolicyName) != nil {
		t.Fatal("no policy without compression")
	}
	if len(off.Absent) != 1 || off.Absent[0].GetKind() != "MigrationPolicy" || off.Absent[0].GetName() != migrationPolicyName {
		t.Fatalf("absent %+v", off.Absent)
	}
	_, on := renderKubeVirt(t, map[string]string{"migration.compression": "zstd"})
	policy := findObject(on.Objects, "MigrationPolicy", migrationPolicyName)
	if policy == nil || policy.GetAPIVersion() != "migrations.kubevirt.io/v1alpha1" || policy.GetNamespace() != "" {
		t.Fatalf("objects %+v", on.Objects)
	}
	spec, _, _ := unstructured.NestedMap(policy.Object, "spec")
	want := map[string]any{"selectors": map[string]any{}, "experimental": map[string]any{"compression": "zstd"}}
	if !reflect.DeepEqual(spec, want) || len(on.Absent) != 0 {
		t.Fatalf("spec %+v absent %+v", spec, on.Absent)
	}
}

func TestRenderVirtualizationEviction(t *testing.T) {
	for setting, want := range map[string]string{"": "LiveMigrateIfPossible", "LiveMigrate": "LiveMigrate", "None": "None"} {
		kv, _ := renderKubeVirt(t, map[string]string{"virt.eviction-strategy": setting})
		if got, _, _ := unstructured.NestedString(kv.Object, "spec", "configuration", "evictionStrategy"); got != want {
			t.Errorf("%q gave %q", setting, got)
		}
	}
	_, err := RenderVirtualization(AddonInput{Settings: map[string]string{"virt.emulation": "false", "virt.eviction-strategy": "Always"}})
	if err == nil || !strings.Contains(err.Error(), "virt.eviction-strategy") {
		t.Fatalf("error %v", err)
	}
}

func TestRenderVirtualizationSupportContainerResources(t *testing.T) {
	kv, _ := renderKubeVirt(t, map[string]string{})
	got, _, _ := unstructured.NestedSlice(kv.Object, "spec", "configuration", "supportContainerResources")
	want := []any{map[string]any{
		"type":      "guest-console-log",
		"resources": map[string]any{"limits": map[string]any{"cpu": "100m", "memory": "120M"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("support container resources %+v", got)
	}
}

func TestRenderVirtualizationKeepsTheHotplugGateToKubeVirt(t *testing.T) {
	kv, _ := renderKubeVirt(t, map[string]string{})
	gates, _, _ := unstructured.NestedStringSlice(kv.Object, "spec", "configuration", "developerConfiguration", "featureGates")
	if slices.Contains(gates, "DeclarativeHotplugVolumes") {
		t.Fatalf("gates %v: KubeVirt 1.9.0 enables DeclarativeHotplugVolumes by default", gates)
	}
}
