package operator

import (
	"maps"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

func managedLabelKeys() []string { return roles.ManagedLabelKeys() }

func RoleLabels(names []string) map[string]string { return roles.Labels(names) }

func RoleTaints(names []string) []corev1.Taint { return roles.Taints(names) }

func ApplyRoles(node *corev1.Node, roleNames []string, managed bool) bool {
	desiredLabels := maps.Clone(node.Labels)
	if desiredLabels == nil {
		desiredLabels = map[string]string{}
	}
	for _, key := range managedLabelKeys() {
		delete(desiredLabels, key)
	}
	maps.Copy(desiredLabels, RoleLabels(roleNames))
	desiredLabels[v1alpha1.LabelManaged] = strconv.FormatBool(managed)

	desiredTaints := slices.DeleteFunc(slices.Clone(node.Spec.Taints), func(t corev1.Taint) bool { return t.Key == roles.NoWorkloadTaint })
	desiredTaints = append(desiredTaints, RoleTaints(roleNames)...)

	changed := !maps.Equal(node.Labels, desiredLabels) || !taintsEqual(node.Spec.Taints, desiredTaints)
	node.Labels = desiredLabels
	node.Spec.Taints = desiredTaints
	return changed
}

type taintKey struct {
	Key    string
	Value  string
	Effect corev1.TaintEffect
}

func taintSet(taints []corev1.Taint) map[taintKey]int {
	set := make(map[taintKey]int, len(taints))
	for _, t := range taints {
		set[taintKey{Key: t.Key, Value: t.Value, Effect: t.Effect}]++
	}
	return set
}

func taintsEqual(a, b []corev1.Taint) bool {
	return maps.Equal(taintSet(a), taintSet(b))
}
