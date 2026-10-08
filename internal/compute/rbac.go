package compute

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func rule(group string, resources []string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs}
}

func OperatorRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("kubevirt.io", []string{"virtualmachines"}, "create", "delete", "get", "list", "patch", "update", "watch"),
		rule("kubevirt.io", []string{"virtualmachineinstances", "virtualmachineinstancemigrations"}, "get", "list", "watch"),
		rule("snapshot.kubevirt.io", []string{"virtualmachinesnapshots"}, "delete", "get", "list", "watch"),
		rule("instancetype.kubevirt.io", []string{"virtualmachineclusterinstancetypes", "virtualmachineclusterpreferences"}, "get", "list", "watch"),
		rule("subresources.kubevirt.io", []string{"virtualmachineinstances/guestosinfo"}, "get"),
		rule("cdi.kubevirt.io", []string{"datavolumes"}, "create", "delete", "get", "list", "patch", "update", "watch"),
		rule("cdi.kubevirt.io", []string{"datasources"}, "get", "list", "watch"),
		rule("", []string{"persistentvolumeclaims"}, "get", "list", "patch", "watch"),
		rule("", []string{"secrets"}, "create", "delete", "get", "list", "patch", "update", "watch"),
		rule("", []string{"pods"}, "get", "list", "patch", "watch"),
		rule("snapshot.storage.k8s.io", []string{"volumesnapshots"}, "create", "delete", "get", "list", "patch", "update", "watch"),
		rule("snapshot.storage.k8s.io", []string{"volumesnapshotclasses"}, "get", "list", "watch"),
		rule("storage.k8s.io", []string{"storageclasses"}, "get", "list", "watch"),
		rule("bedrock.cloudyfolks.io", []string{"virtualmachines/finalizers", "volumes/finalizers", "volumesnapshots/finalizers"}, "update"),
	}
}

func ProxyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("bedrock.cloudyfolks.io", []string{"virtualmachines"}, "get", "list", "patch"),
		rule("", []string{"namespaces"}, "get"),
		rule("kubevirt.io", []string{"virtualmachineinstancemigrations"}, "create", "get", "list"),
		rule("snapshot.kubevirt.io", []string{"virtualmachinesnapshots"}, "create", "get", "list"),
		rule("subresources.kubevirt.io", []string{"virtualmachines/restart"}, "update"),
		rule("subresources.kubevirt.io", []string{"virtualmachineinstances/vnc"}, "get"),
		rule("subresources.kubevirt.io", []string{"virtualmachineinstances/console"}, "get"),
	}
}
