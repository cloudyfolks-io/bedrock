package tenancy

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ConditionNetworkReady   = "NetworkReady"
	ConditionOwnersBound    = "OwnersBound"
	ConditionWithinQuota    = "WithinQuota"
	ConditionAdmitted       = "Admitted"
	ConditionNamespaceReady = "NamespaceReady"
	ConditionSubnetReady    = "SubnetReady"
	ConditionQuotaReady     = "QuotaReady"
	ConditionAccessReady    = "AccessReady"
)

const (
	ReasonAvailable            = "Available"
	ReasonBound                = "Bound"
	ReasonWithinQuota          = "WithinQuota"
	ReasonWaitingForNetwork    = "WaitingForNetwork"
	ReasonApplyFailed          = "ApplyFailed"
	ReasonProjectsOverQuota    = "ProjectsOverQuota"
	ReasonAllocationOverQuota  = "AllocationOverQuota"
	ReasonAdmitted             = "Admitted"
	ReasonTenantNotFound       = "TenantNotFound"
	ReasonProjectQuotaExceeded = "ProjectQuotaExceeded"
	ReasonTenantQuotaExceeded  = "TenantQuotaExceeded"
	ReasonCreated              = "Created"
	ReasonNamespaceConflict    = "NamespaceConflict"
	ReasonNamespaceTerminating = "NamespaceTerminating"
	ReasonReady                = "Ready"
	ReasonWaitingForSubnet     = "WaitingForSubnet"
	ReasonCIDRExhausted        = "CIDRExhausted"
	ReasonApplied              = "Applied"
	ReasonQuotaUnavailable     = "QuotaUnavailable"
	ReasonQuotaRequired        = "QuotaRequired"
	ReasonPending              = "Pending"
)

func withCondition(conditions []metav1.Condition, next metav1.Condition) []metav1.Condition {
	cloned := slices.Clone(conditions)
	v1alpha1.SetCondition(&cloned, next)
	return cloned
}

const (
	ReasonSettled = "Settled"
	ReasonHealthy = "Healthy"
)

func conditionOn(generation int64, conditionType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: status, Reason: reason, Message: message, ObservedGeneration: generation}
}

func withConditions(base []metav1.Condition, next ...metav1.Condition) []metav1.Condition {
	merged := slices.Clone(base)
	for _, condition := range next {
		merged = withCondition(merged, condition)
	}
	return merged
}

func firstNotTrue(generation int64, dependencies ...metav1.Condition) metav1.Condition {
	for _, dependency := range dependencies {
		if dependency.Status != metav1.ConditionTrue {
			return conditionOn(generation, v1alpha1.ConditionReady, metav1.ConditionFalse, dependency.Reason, dependency.Message)
		}
	}
	return conditionOn(generation, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady, "")
}

func flagged(generation int64, conditionType, idleReason string, conditions []metav1.Condition, reasons ...string) metav1.Condition {
	for _, condition := range conditions {
		if condition.Status == metav1.ConditionFalse && slices.Contains(reasons, condition.Reason) {
			return conditionOn(generation, conditionType, metav1.ConditionTrue, condition.Reason, condition.Message)
		}
	}
	return conditionOn(generation, conditionType, metav1.ConditionFalse, idleReason, "")
}
