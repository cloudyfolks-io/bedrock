package network

import (
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	waitRequeue        = 5 * time.Second
	terminatingRequeue = 10 * time.Second
)

func trueCondition(conditionType string, generation int64, reason string) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: reason, ObservedGeneration: generation}
}

func falseCondition(conditionType string, generation int64, reason, message string) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: metav1.ConditionFalse, Reason: reason, Message: message, ObservedGeneration: generation}
}

func violationCondition(conditionType string, generation int64, v *Violation) metav1.Condition {
	return falseCondition(conditionType, generation, v.Reason, v.Message)
}

func withCondition(existing []metav1.Condition, cond metav1.Condition) []metav1.Condition {
	conditions := slices.Clone(existing)
	v1alpha1.SetCondition(&conditions, cond)
	return conditions
}
