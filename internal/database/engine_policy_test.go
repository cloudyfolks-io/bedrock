package database

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestTheEngineNetworkPolicySelectsEveryEngineByRole(t *testing.T) {
	policy := EnginesPolicyObject()
	if policy.Spec.PodSelector.MatchLabels[LabelRole] != RoleEngine || len(policy.Spec.PodSelector.MatchLabels) != 1 {
		t.Fatalf("selector %v", policy.Spec.PodSelector.MatchLabels)
	}
	one, err := EnginePolicyObject(v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{UID: "u1"}})
	must(t, err)
	from, _, _ := unstructured.NestedSlice(one.Object, "spec", "ingress")
	peer := from[0].(map[string]any)["from"].([]any)[0].(map[string]any)["podSelector"].(map[string]any)
	if labels := peer["matchLabels"].(map[string]any); labels[LabelInstance] != "u1" || len(labels) != 1 || peer["matchExpressions"] != nil {
		t.Fatalf("peers of one database %v", peer)
	}
}

func TestEveryEngineKindWritesTheRoleLabelOnItsPods(t *testing.T) {
	w := valkeyWorld(t)
	w.reconcile()
	if got := w.valkeyDeployment().Spec.Template.Labels[LabelRole]; got != RoleEngine {
		t.Errorf("engine pods carry role %q", got)
	}
}
