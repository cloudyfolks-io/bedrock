package network

import (
	"context"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
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

func withRangePolicy(existing []metav1.Condition, generation int64, breach *Violation) []metav1.Condition {
	conditions := slices.Clone(existing)
	if breach == nil {
		meta.RemoveStatusCondition(&conditions, ConditionRangePolicy)
		return conditions
	}
	v1alpha1.SetCondition(&conditions, violationCondition(ConditionRangePolicy, generation, breach))
	return conditions
}

func applyStatus(ctx context.Context, c client.Client, body client.Object) error {
	if err := ssa.ApplyStatus(ctx, c, body, v1alpha1.NetworkFieldManager); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "write the status", "object", client.ObjectKeyFromObject(body).String())
		return err
	}
	return nil
}
