package operator

import (
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func verbsOn(rules []rbacv1.PolicyRule, group, resource string) []string {
	var verbs []string
	for _, rule := range rules {
		if slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) {
			verbs = append(verbs, rule.Verbs...)
		}
	}
	slices.Sort(verbs)
	return slices.Compact(verbs)
}

func TestNetworkRulesGrantEveryFabricKindWeWrite(t *testing.T) {
	full := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	for _, resource := range []string{"vpcs", "subnets", "ovn-eips", "ovn-fips", "router-lb-rules", "switch-lb-rules", "vips", "dns-zones", "security-groups", "provider-networks", "vlans", "bgp-confs"} {
		if got := verbsOn(networkRules(), "fabric.cloudyfolks.io", resource); !slices.Equal(got, full) {
			t.Fatalf("%s: %v", resource, got)
		}
	}
}

func TestNetworkRulesFabricIPsAreReadOnly(t *testing.T) {
	if got := verbsOn(networkRules(), "fabric.cloudyfolks.io", "ips"); !slices.Equal(got, []string{"get", "list", "watch"}) {
		t.Fatalf("ips: %v", got)
	}
}

func TestNetworkRulesNeverUseWildcards(t *testing.T) {
	for _, rule := range networkRules() {
		for _, list := range [][]string{rule.APIGroups, rule.Resources, rule.Verbs} {
			if slices.Contains(list, "*") {
				t.Fatalf("wildcard in %+v", rule)
			}
		}
	}
}

func TestNetworkRulesCoverTheFinalizersOfOwnerReferences(t *testing.T) {
	for _, resource := range []string{"subnets", "securitygroups"} {
		if got := verbsOn(networkRules(), "bedrock.cloudyfolks.io", resource+"/finalizers"); !slices.Equal(got, []string{"update"}) {
			t.Fatalf("%s/finalizers: %v", resource, got)
		}
	}
}

func TestNetworkRulesSkipFinalizersThatNothingNeeds(t *testing.T) {
	for _, resource := range []string{"networks", "elasticips", "loadbalancers", "dnszones"} {
		if got := verbsOn(networkRules(), "bedrock.cloudyfolks.io", resource+"/finalizers"); len(got) != 0 {
			t.Fatalf("%s/finalizers: %v", resource, got)
		}
	}
}

func TestNetworkRulesCoverAttachmentsAndPolicies(t *testing.T) {
	full := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	if got := verbsOn(networkRules(), "k8s.cni.cncf.io", "network-attachment-definitions"); !slices.Equal(got, full) {
		t.Fatalf("attachments: %v", got)
	}
	if got := verbsOn(networkRules(), "networking.k8s.io", "networkpolicies"); !slices.Equal(got, full) {
		t.Fatalf("policies: %v", got)
	}
	read := []string{"get", "list", "watch"}
	if got := verbsOn(networkRules(), "", "namespaces"); !slices.Equal(got, read) {
		t.Fatalf("namespaces: %v", got)
	}
	if got := verbsOn(networkRules(), "apps", "daemonsets"); !slices.Equal(got, read) {
		t.Fatalf("daemonsets: %v", got)
	}
}
