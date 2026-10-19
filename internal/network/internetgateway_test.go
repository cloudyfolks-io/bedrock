package network

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ruleSpecs(rules []*unstructured.Unstructured) []string {
	out := make([]string, len(rules))
	for i, rule := range rules {
		cidr, _, _ := unstructured.NestedString(rule.Object, "spec", "v4IpCidr")
		out[i] = rule.GetName() + "=" + cidr
	}
	return out
}

func TestSnatRuleNameIsStableAndPerNetwork(t *testing.T) {
	got := SnatRuleName("acme", "10.0.0.0/8")
	if got != SnatRuleName("acme", "10.0.0.0/8") {
		t.Fatal("the same input must give the same name")
	}
	if !strings.HasPrefix(got, "acme-igw-") {
		t.Fatalf("name %q", got)
	}
	if got == SnatRuleName("acme", "172.16.0.0/12") || got == SnatRuleName("other", "10.0.0.0/8") {
		t.Fatal("a different range or Network must give a different name")
	}
}

func TestSnatRuleNameFitsALabelValue(t *testing.T) {
	got := SnatRuleName(strings.Repeat("n", 63), "192.168.0.0/16")
	if len(got) > 63 || !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(got) {
		t.Fatalf("name %q", got)
	}
}

func TestRangeSnatRulesDoNotDependOnTheOrderOfTheRanges(t *testing.T) {
	in := SnatInput{Network: "acme", EIP: "eip"}
	forward := ruleSpecs(RangeSnatRules(in, []string{"10.0.0.0/8", "192.168.0.0/16"}))
	backward := ruleSpecs(RangeSnatRules(in, []string{"192.168.0.0/16", "10.0.0.0/8"}))
	slices.Sort(forward)
	slices.Sort(backward)
	if !slices.Equal(forward, backward) || len(forward) != 2 {
		t.Fatalf("forward %v backward %v", forward, backward)
	}
}

func TestRangeSnatRulesWriteTheFabricFields(t *testing.T) {
	in := SnatInput{Network: "acme", EIP: "ns-eip-abc", Labels: map[string]string{"a": "b"}, Annotations: map[string]string{"c": "d"}}
	rules := RangeSnatRules(in, []string{"10.0.0.0/8"})
	if len(rules) != 1 {
		t.Fatalf("rules %d", len(rules))
	}
	spec, _, _ := unstructured.NestedMap(rules[0].Object, "spec")
	if spec["ovnEip"] != "ns-eip-abc" || spec["vpc"] != "acme" || spec["v4IpCidr"] != "10.0.0.0/8" || len(spec) != 3 {
		t.Fatalf("spec %v", spec)
	}
	if rules[0].GetLabels()["a"] != "b" || rules[0].GetAnnotations()["c"] != "d" {
		t.Fatalf("metadata %v %v", rules[0].GetLabels(), rules[0].GetAnnotations())
	}
}

func TestRangeSnatRulesSkipRangesThatAreNotIPv4(t *testing.T) {
	rules := RangeSnatRules(SnatInput{Network: "acme", EIP: "e"}, []string{"fd00::/48", "10.0.0.0/8", "not-a-range"})
	if len(rules) != 1 {
		t.Fatalf("rules %v", ruleSpecs(rules))
	}
}

func TestSubnetSnatRuleWritesTheSubnetForm(t *testing.T) {
	rule := SubnetSnatRule(SnatInput{Network: "acme", EIP: "e"}, "ns-default-abc")
	spec, _, _ := unstructured.NestedMap(rule.Object, "spec")
	if spec["ovnEip"] != "e" || spec["vpcSubnet"] != "ns-default-abc" || len(spec) != 2 {
		t.Fatalf("spec %v", spec)
	}
	if rule.GetName() != SnatRuleName("acme", "ns-default-abc") {
		t.Fatalf("name %q", rule.GetName())
	}
}
