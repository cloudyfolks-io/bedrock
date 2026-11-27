package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/internal/clusters"
)

func clustersRules() []rbacv1.PolicyRule {
	return clusters.OperatorRules()
}
