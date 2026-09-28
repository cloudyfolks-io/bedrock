package roles

import (
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	LabelPrefix     = "bedrock.cloudyfolks.io/role-"
	FabricGWLabel   = "fabric.cloudyfolks.io/external-gw"
	FabricRoleLabel = "fabric/role"
	NoWorkloadTaint = "bedrock.cloudyfolks.io/no-workload"
)

func ManagedLabelKeys() []string {
	keys := []string{FabricGWLabel, FabricRoleLabel, v1alpha1.LabelManaged}
	for _, role := range v1alpha1.AllRoles() {
		keys = append(keys, LabelPrefix+role)
	}
	return keys
}

func Labels(names []string) map[string]string {
	labels := map[string]string{}
	for _, role := range names {
		labels[LabelPrefix+role] = "true"
	}
	if slices.Contains(names, v1alpha1.RoleFabricGateway) {
		labels[FabricGWLabel] = "true"
	}
	if slices.Contains(names, v1alpha1.RoleControlPlane) {
		labels[FabricRoleLabel] = "master"
	}
	return labels
}

func Taints(names []string) []corev1.Taint {
	if slices.Contains(names, v1alpha1.RoleWorkload) {
		return nil
	}
	return []corev1.Taint{{Key: NoWorkloadTaint, Effect: corev1.TaintEffectNoSchedule}}
}

func FromLabels(labels map[string]string) []string {
	var out []string
	for _, role := range v1alpha1.AllRoles() {
		if labels[LabelPrefix+role] == "true" {
			out = append(out, role)
		}
	}
	return out
}
