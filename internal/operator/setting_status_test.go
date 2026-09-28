package operator

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSettingStatusDoesNotMutateInput(t *testing.T) {
	setting := v1alpha1.Setting{
		ObjectMeta: metav1.ObjectMeta{Name: "storage.replicas", Generation: 2},
		Spec:       v1alpha1.SettingSpec{Value: "0"},
		Status: v1alpha1.SettingStatus{
			ObservedGeneration: 2,
			Applied:            "3",
		},
	}
	v1alpha1.SetCondition(&setting.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Valid",
		ObservedGeneration: 2,
	})

	next := settingStatus(setting)

	if len(setting.Status.Conditions) != 1 || setting.Status.Conditions[0].Status != metav1.ConditionTrue || setting.Status.Conditions[0].Reason != "Valid" {
		t.Fatalf("input status was mutated, got %+v", setting.Status.Conditions)
	}
	if v1alpha1.IsConditionTrue(next.Conditions, v1alpha1.ConditionReady) {
		t.Fatalf("returned status should not be Ready, got %+v", next.Conditions)
	}
	if len(next.Conditions) != 1 || next.Conditions[0].Reason != "Invalid" {
		t.Fatalf("expected Invalid reason, got %+v", next.Conditions)
	}
	if next.Applied != "" {
		t.Fatalf("expected Applied to be cleared, got %q", next.Applied)
	}
	if statusEqual(setting.Status, next) {
		t.Fatal("statusEqual should report a difference between input and returned status")
	}
}
