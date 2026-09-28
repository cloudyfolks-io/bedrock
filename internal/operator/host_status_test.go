package operator

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestHostStatusDoesNotMutateInput(t *testing.T) {
	host := v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", Generation: 2},
	}
	v1alpha1.SetCondition(&host.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "NodeMissing",
		ObservedGeneration: 2,
	})
	host.Status.ObservedGeneration = 2

	node := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.30.0"}}}

	next := hostStatus(host, node, metav1.ConditionTrue, "RolesApplied", "")

	if len(host.Status.Conditions) != 1 || host.Status.Conditions[0].Status != metav1.ConditionFalse || host.Status.Conditions[0].Reason != "NodeMissing" {
		t.Fatalf("input status was mutated, got %+v", host.Status.Conditions)
	}
	if !v1alpha1.IsConditionTrue(next.Conditions, v1alpha1.ConditionReady) {
		t.Fatalf("expected returned status to be Ready, got %+v", next.Conditions)
	}
	if len(next.Conditions) != 1 || next.Conditions[0].Reason != "RolesApplied" {
		t.Fatalf("expected RolesApplied reason, got %+v", next.Conditions)
	}
	if next.KubernetesVersion != "v1.30.0" {
		t.Fatalf("expected kubelet version from node, got %q", next.KubernetesVersion)
	}
	if hostStatusEqual(host.Status, next) {
		t.Fatal("hostStatusEqual should report a difference between input and returned status")
	}
}

func TestHostStatusClearsVersionWhenNodeMissing(t *testing.T) {
	host := v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", Generation: 2},
		Status:     v1alpha1.HostStatus{KubernetesVersion: "v1.30.0"},
	}

	next := hostStatus(host, nil, metav1.ConditionFalse, "NodeMissing", "no Node with this name")

	if next.KubernetesVersion != "" {
		t.Fatalf("expected empty KubernetesVersion when node is missing, got %q", next.KubernetesVersion)
	}
}

func TestHostStatusEqualIgnoresTransitionTime(t *testing.T) {
	a := v1alpha1.HostStatus{
		ObservedGeneration: 3,
		KubernetesVersion:  "v1.30.0",
		Conditions: []metav1.Condition{{
			Type:               v1alpha1.ConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             "RolesApplied",
			Message:            "",
			LastTransitionTime: metav1.NewTime(time.Now()),
		}},
	}
	b := v1alpha1.HostStatus{
		ObservedGeneration: 3,
		KubernetesVersion:  "v1.30.0",
		Conditions: []metav1.Condition{{
			Type:               v1alpha1.ConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             "RolesApplied",
			Message:            "",
			LastTransitionTime: metav1.NewTime(time.Now().Add(time.Hour)),
		}},
	}

	if !hostStatusEqual(a, b) {
		t.Fatalf("expected statuses differing only by LastTransitionTime to be equal, got %+v vs %+v", a, b)
	}
}
