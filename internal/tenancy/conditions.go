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
	ReasonQuotaCRDMissing      = "QuotaCRDMissing"
	ReasonPending              = "Pending"
)

func withCondition(conditions []metav1.Condition, next metav1.Condition) []metav1.Condition {
	cloned := slices.Clone(conditions)
	v1alpha1.SetCondition(&cloned, next)
	return cloned
}
