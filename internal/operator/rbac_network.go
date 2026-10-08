package operator

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

func networkRules() []rbacv1.PolicyRule {
	read := []string{"get", "list", "watch"}
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"fabric.cloudyfolks.io"},
			Resources: []string{"bgp-confs", "dns-zones", "ovn-eips", "ovn-fips", "provider-networks", "router-lb-rules", "security-groups", "subnets", "vlans", "vpcs"},
			Verbs:     standardVerbs(),
		},
		{APIGroups: []string{"fabric.cloudyfolks.io"}, Resources: []string{"ips"}, Verbs: read},
		{APIGroups: []string{"k8s.cni.cncf.io"}, Resources: []string{"network-attachment-definitions"}, Verbs: standardVerbs()},
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: standardVerbs()},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: read},
		{APIGroups: []string{"apps"}, Resources: []string{"daemonsets"}, Verbs: read},
		{
			APIGroups: []string{"bedrock.cloudyfolks.io"},
			Resources: []string{"securitygroups/finalizers", "subnets/finalizers"},
			Verbs:     []string{"update"},
		},
	}
}
