package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/internal/container"
)

func containerRules() []rbacv1.PolicyRule {
	return container.OperatorRules()
}
