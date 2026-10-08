package backup

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Scope struct{ Namespace, Tenant, Project string }

func ScopeOf(ns corev1.Namespace) (Scope, bool) {
	tenant, project := ns.Labels[v1alpha1.LabelTenant], ns.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return Scope{}, false
	}
	return Scope{Namespace: ns.Name, Tenant: tenant, Project: project}, true
}
