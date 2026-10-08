package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/internal/compute"
)

func computeRules() []rbacv1.PolicyRule {
	return compute.OperatorRules()
}
