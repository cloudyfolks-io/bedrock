package images

import (
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func UploadHandlerRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"upload.cdi.kubevirt.io"}, Resources: []string{"uploadtokenrequests"}, Verbs: []string{"create"}},
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"images"}, Verbs: []string{"get"}},
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"settings"}, Verbs: []string{"get", "list"}},
	}
}
