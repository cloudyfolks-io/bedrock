package operator

import (
	"slices"
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
