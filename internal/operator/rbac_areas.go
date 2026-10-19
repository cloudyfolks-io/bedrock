package operator

import (
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
)

func areaRules() []rbacv1.PolicyRule {
	return slices.Concat(tenancyRules(), imagesRules(), computeRules(), containerRules(), networkRules(), backupRules(), objectStorageRules(), addonsRules(), registryRules())
}
