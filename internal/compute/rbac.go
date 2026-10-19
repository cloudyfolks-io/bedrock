package compute

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func rule(group string, resources []string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs}
}

func OperatorRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("kubevirt.io", []string{"virtualmachines"}, "create", "get", "list", "patch", "watch"),
		rule("kubevirt.io", []string{"virtualmachineinstances"}, "get", "list", "watch"),
		rule("kubevirt.io", []string{"virtualmachineinstancemigrations"}, "list", "watch"),
		rule("snapshot.kubevirt.io", []string{"virtualmachinesnapshots"}, "delete", "list"),
		rule("instancetype.kubevirt.io", []string{"virtualmachineclusterinstancetypes", "virtualmachineclusterpreferences"}, "get"),
		rule("subresources.kubevirt.io", []string{"virtualmachineinstances/guestosinfo"}, "get"),
		rule("cdi.kubevirt.io", []string{"datavolumes"}, "create", "get", "list", "watch"),
		rule("", []string{"persistentvolumeclaims"}, "list", "patch", "watch"),
		rule("", []string{"secrets"}, "create", "get", "patch"),
		rule("", []string{"pods"}, "list", "patch", "watch"),
		rule("snapshot.storage.k8s.io", []string{"volumesnapshots"}, "create", "get", "list", "watch"),
		rule("snapshot.storage.k8s.io", []string{"volumesnapshotclasses"}, "list"),
		rule("storage.k8s.io", []string{"storageclasses"}, "list", "watch"),
		rule("fabric.cloudyfolks.io", []string{"security-groups"}, "get"),
		rule("fabric.cloudyfolks.io", []string{"ips"}, "get", "list", "watch"),
		rule("bedrock.cloudyfolks.io", []string{"virtualmachines/finalizers", "volumes/finalizers", "volumesnapshots/finalizers"}, "update"),
	}
}

func ProxyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("bedrock.cloudyfolks.io", []string{"virtualmachines"}, "get", "patch"),
		rule("", []string{"namespaces"}, "get"),
		rule("kubevirt.io", []string{"virtualmachineinstancemigrations"}, "create"),
		rule("snapshot.kubevirt.io", []string{"virtualmachinesnapshots"}, "create", "list"),
		rule("subresources.kubevirt.io", []string{"virtualmachines/restart"}, "update"),
		rule("subresources.kubevirt.io", []string{"virtualmachineinstances/vnc"}, "get"),
		rule("subresources.kubevirt.io", []string{"virtualmachineinstances/console"}, "get"),
	}
}
