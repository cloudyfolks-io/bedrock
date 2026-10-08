package tenancy

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWithConditionReplacesTheSameType(t *testing.T) {
	in := []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "x"},
		{Type: "NetworkReady", Status: metav1.ConditionTrue, Reason: "y"},
	}
	got := withCondition(in, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "z"})
	if len(got) != 2 {
		t.Fatalf("conditions = %v", got)
	}
	for _, c := range got {
		if c.Type == "Ready" && c.Status != metav1.ConditionTrue {
			t.Fatalf("Ready not replaced: %v", got)
		}
	}
	if in[0].Status != metav1.ConditionFalse {
		t.Fatalf("input changed: %v", in)
	}
}
