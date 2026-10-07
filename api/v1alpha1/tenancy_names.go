package v1alpha1

const (
	RoleProjectOwner  = "bedrock-project-owner"
	RoleProjectEditor = "bedrock-project-editor"
	RoleProjectViewer = "bedrock-project-viewer"
)

func TenantOwnerClusterRole(tenant string) string {
	return "tenant:" + tenant + ":owner"
}

func ProjectNamespace(tenant, project string) string {
	return tenant + "-" + project
}

func DefaultNetworkName(tenant string) string {
	return tenant
}

func TenantLabels(kind, name, tenant, project string) map[string]string {
	labels := map[string]string{
		LabelKind:   kind,
		LabelName:   name,
		LabelTenant: tenant,
	}
	if project != "" {
		labels[LabelProject] = project
	}
	return labels
}
