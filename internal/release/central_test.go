package release

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func centralDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "ovn-central", "namespace": "kube-system"},
		"spec": map[string]any{"replicas": int64(1), "template": map[string]any{"spec": map[string]any{
			"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
				"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "fabric/role", "operator": "In", "values": []any{"master"}}}}},
			}}},
		}}},
	}}
}

func centralOf(t *testing.T, bundle Bundle) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range groupObjects(bundle, "fabric") {
		if obj.GetKind() == "Deployment" && obj.GetName() == "ovn-central" {
			return obj
		}
	}
	t.Fatal("no ovn-central")
	return nil
}

func centralTerms(obj *unstructured.Unstructured) []any {
	terms, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
	return terms
}

func pinnedNames(obj *unstructured.Unstructured) [][]string {
	var out [][]string
	for _, term := range centralTerms(obj) {
		fields, _ := term.(map[string]any)["matchFields"].([]any)
		var names []string
		for _, field := range fields {
			values, _ := field.(map[string]any)["values"].([]any)
			for _, value := range values {
				names = append(names, value.(string))
			}
		}
		out = append(out, names)
	}
	return out
}

func TestCentralReplicasFollowTheMasterAddresses(t *testing.T) {
	bundle := fabricGroupOf(centralDeployment())
	out := SetCentralTopology(bundle, map[string]string{VarMasterIPs: "10.0.0.1,10.0.0.2,10.0.0.3"})
	replicas, _, _ := unstructured.NestedInt64(centralOf(t, out).Object, "spec", "replicas")
	if replicas != 3 {
		t.Fatalf("replicas %d", replicas)
	}
	if before, _, _ := unstructured.NestedInt64(centralOf(t, bundle).Object, "spec", "replicas"); before != 1 {
		t.Fatal("the input bundle must not change")
	}
}

func TestCentralPodsPinToTheListedNodes(t *testing.T) {
	out := SetCentralTopology(fabricGroupOf(centralDeployment()), map[string]string{VarMasterIPs: "10.0.0.1,10.0.0.2", VarCentralNodes: "host-1,host-2"})
	got := pinnedNames(centralOf(t, out))
	if len(got) != 2 || !slices.Equal(got[0], []string{"host-1"}) || !slices.Equal(got[1], []string{"host-2"}) {
		t.Fatalf("pinned %v", got)
	}
	for _, term := range centralTerms(centralOf(t, out)) {
		if term.(map[string]any)["matchExpressions"] == nil {
			t.Fatal("the master label must stay in every term")
		}
	}
}

func TestCentralPinCreatesTheTermWhenThereIsNone(t *testing.T) {
	bare := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "ovn-central", "namespace": "kube-system"},
		"spec":     map[string]any{"replicas": int64(1)},
	}}
	out := SetCentralTopology(fabricGroupOf(bare), map[string]string{VarMasterIPs: "10.0.0.1", VarCentralNodes: "host-1"})
	if got := pinnedNames(centralOf(t, out)); len(got) != 1 || !slices.Equal(got[0], []string{"host-1"}) {
		t.Fatalf("pinned %v", got)
	}
}

func TestCentralTopologyWithoutAddressesKeepsTheRender(t *testing.T) {
	out := SetCentralTopology(fabricGroupOf(centralDeployment()), map[string]string{})
	if replicas, _, _ := unstructured.NestedInt64(centralOf(t, out).Object, "spec", "replicas"); replicas != 1 {
		t.Fatalf("replicas %d", replicas)
	}
}

func TestRealFabricRenderBakesOneCentralReplica(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	if replicas, _, _ := unstructured.NestedInt64(centralOf(t, bundle).Object, "spec", "replicas"); replicas != 1 {
		t.Fatalf("the chart cannot count placeholder addresses, replicas %d", replicas)
	}
	out := SetCentralTopology(bundle, map[string]string{VarMasterIPs: "10.0.0.1,10.0.0.2,10.0.0.3", VarCentralNodes: "a,b,c"})
	if replicas, _, _ := unstructured.NestedInt64(centralOf(t, out).Object, "spec", "replicas"); replicas != 3 {
		t.Fatalf("replicas %d", replicas)
	}
	if len(pinnedNames(centralOf(t, out))) == 0 {
		t.Fatal("no pinned term")
	}
}

func TestWithMastersSetsAddressesAndNames(t *testing.T) {
	vars := map[string]string{VarVIP: "1.1.1.1"}
	out := WithMasters(vars, []MasterNode{{"b", "10.0.0.2"}, {"a", "10.0.0.1"}})
	if out[VarMasterIPs] != "10.0.0.2,10.0.0.1" || out[VarCentralNodes] != "b,a" || out[VarVIP] != "1.1.1.1" {
		t.Fatalf("vars %v", out)
	}
	if _, ok := vars[VarMasterIPs]; ok {
		t.Fatal("the input must not change")
	}
}
