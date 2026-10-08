package network

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const sgNamespace = "t1-prod"

func tcpRule(port int32, cidr string) v1alpha1.SecurityGroupRule {
	return v1alpha1.SecurityGroupRule{Protocol: "tcp", Port: port, CIDR: cidr}
}

func normalized(t *testing.T, rules ...v1alpha1.SecurityGroupRule) []Rule {
	t.Helper()
	return NormalizeRules(rules)
}

func fabricRules(t *testing.T, rules ...v1alpha1.SecurityGroupRule) []fabric.SGRule {
	t.Helper()
	got, violation := FabricRules(sgNamespace, normalized(t, rules...))
	if violation != nil {
		t.Fatalf("unexpected violation %+v", violation)
	}
	return got
}

func TestFabricRulesAddressRule(t *testing.T) {
	got := fabricRules(t, tcpRule(443, "0.0.0.0/0"))
	want := fabric.SGRule{IPVersion: "ipv4", Protocol: "tcp", RemoteType: "address", RemoteAddress: "0.0.0.0/0", Policy: "allow", Priority: 100, PortMin: 443, PortMax: 443}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
}

func TestFabricRulesPortRange(t *testing.T) {
	got := fabricRules(t, v1alpha1.SecurityGroupRule{Protocol: "udp", Port: 5000, EndPort: 5010, CIDR: "10.0.0.0/8"})
	if got[0].PortMin != 5000 || got[0].PortMax != 5010 {
		t.Fatalf("got %+v", got)
	}
}

func TestFabricRulesICMPAndAllHaveNoPorts(t *testing.T) {
	got := fabricRules(t, v1alpha1.SecurityGroupRule{Protocol: "icmp", CIDR: "10.0.0.0/8"}, v1alpha1.SecurityGroupRule{CIDR: "10.1.0.0/16"})
	if got[0].Protocol != "icmp" || got[1].Protocol != "all" || got[0].PortMin != 0 || got[1].PortMax != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestFabricRulesSecurityGroupRef(t *testing.T) {
	got := fabricRules(t, v1alpha1.SecurityGroupRule{Protocol: "tcp", Port: 5432, SecurityGroupRef: "web"})
	if got[0].RemoteType != "securityGroup" || got[0].RemoteSecurityGroup != FabricName(sgNamespace, "web") || got[0].RemoteAddress != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestFabricRulesRefuseIPv6(t *testing.T) {
	_, v := FabricRules(sgNamespace, normalized(t, v1alpha1.SecurityGroupRule{CIDR: "fd00::/64"}))
	if v == nil || v.Reason != ReasonUnsupported || !strings.Contains(v.Message, "IPv6") {
		t.Fatalf("violation %+v", v)
	}
}

func TestFabricRulesRefuseBadPorts(t *testing.T) {
	for name, rule := range map[string]Rule{
		"zero min":    {CIDR: "10.0.0.0/8", Protocol: "tcp", PortMin: 0, PortMax: 80},
		"too high":    {CIDR: "10.0.0.0/8", Protocol: "tcp", PortMin: 80, PortMax: 70000},
		"backwards":   {CIDR: "10.0.0.0/8", Protocol: "tcp", PortMin: 90, PortMax: 80},
		"icmp port":   {CIDR: "10.0.0.0/8", Protocol: "icmp", PortMin: 1, PortMax: 1},
		"unknown":     {CIDR: "10.0.0.0/8", Protocol: "gre"},
		"bad address": {CIDR: "nope", Protocol: "tcp", PortMin: 1, PortMax: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, v := FabricRules(sgNamespace, []Rule{rule}); v == nil || v.Reason != ReasonInvalidRules {
				t.Fatalf("violation %+v", v)
			}
		})
	}
}

func TestFabricRulesRefuseCIDRAndRefTogether(t *testing.T) {
	for _, rule := range []Rule{{CIDR: "10.0.0.0/8", SecurityGroup: "web", Protocol: "all"}, {Protocol: "all"}} {
		if _, v := FabricRules(sgNamespace, []Rule{rule}); v == nil || v.Reason != ReasonInvalidRules {
			t.Fatalf("violation %+v", v)
		}
	}
}

func TestDefaultEgressAllowAllWhenNoEgressRules(t *testing.T) {
	got := EgressOrDefault(nil)
	want := fabric.SGRule{IPVersion: "ipv4", Protocol: "all", RemoteType: "address", RemoteAddress: "0.0.0.0/0", Policy: "allow", Priority: 100}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
}

func TestNoDefaultEgressWhenEgressRulesExist(t *testing.T) {
	rules := fabricRules(t, tcpRule(443, "10.0.0.0/8"))
	if got := EgressOrDefault(rules); len(got) != 1 || got[0].PortMin != 443 {
		t.Fatalf("got %+v", got)
	}
}

func TestPolicyForSelectsOnlyLabeledPods(t *testing.T) {
	np := PolicyFor(sgNamespace, "web", nil, nil, nil)
	if np.Name != "bedrock-sg-web" || np.Namespace != sgNamespace {
		t.Fatalf("identity %s/%s", np.Namespace, np.Name)
	}
	labels := np.Spec.PodSelector.MatchLabels
	if len(labels) != 1 || labels["sg.bedrock.cloudyfolks.io/web"] != "true" || len(np.Spec.PodSelector.MatchExpressions) != 0 {
		t.Fatalf("selector %+v", np.Spec.PodSelector)
	}
	if !slices.Equal(np.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}) {
		t.Fatalf("policy types %v", np.Spec.PolicyTypes)
	}
}

func TestPolicyCoversOnlyTenantNICs(t *testing.T) {
	np := PolicyFor(sgNamespace, "web", nil, nil, []string{"other", "default"})
	if got := np.Annotations["fabric.cloudyfolks.io/network_policy_for"]; got != "t1-prod/default,t1-prod/other" {
		t.Fatalf("annotation %q", got)
	}
}

func TestPolicyWithNoSubnetsMatchesNoNIC(t *testing.T) {
	np := PolicyFor(sgNamespace, "web", nil, nil, nil)
	if got := np.Annotations["fabric.cloudyfolks.io/network_policy_for"]; got != "t1-prod/none" {
		t.Fatalf("annotation %q", got)
	}
}

func TestDefaultSecurityGroupDeniesIngressAndAllowsEgress(t *testing.T) {
	labels := ObjectLabels("Namespace", sgNamespace, "t1", "prod")
	u := DefaultSecurityGroup(sgNamespace, labels)
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	if u.GetName() != "default-t1-prod" || spec["allowSameGroupTraffic"] != false {
		t.Fatalf("group %v", u.Object)
	}
	if _, found := spec["ingressRules"]; found {
		t.Fatal("no ingress rules")
	}
	egress, _ := spec["egressRules"].([]any)
	if len(egress) != 1 {
		t.Fatalf("egress %v", spec["egressRules"])
	}
	rule := egress[0].(map[string]any)
	if rule["remoteAddress"] != "0.0.0.0/0" || rule["protocol"] != "all" || rule["policy"] != "allow" || rule["priority"] != int64(100) {
		t.Fatalf("rule %v", rule)
	}
	if len(u.GetAnnotations()) != 0 || u.GetLabels()[LabelManaged] != "true" {
		t.Fatalf("annotations %v labels %v", u.GetAnnotations(), u.GetLabels())
	}
}

type Probe struct {
	Direction string
	Protocol  string
	Port      int
	Remote    string
	RemoteIn  []string
}

func protocolMatches(rule, probe string) bool {
	return rule == "all" || rule == probe
}

func portMatches(min, max, probePort int) bool {
	return (min == 0 && max == 0) || (probePort >= min && probePort <= max)
}

func AllowedByFabric(rules []fabric.SGRule, p Probe) bool {
	for _, r := range rules {
		if r.Policy != "allow" || !protocolMatches(r.Protocol, p.Protocol) || !portMatches(r.PortMin, r.PortMax, p.Port) {
			continue
		}
		if r.RemoteType == "address" && contains4(r.RemoteAddress, p.Remote) {
			return true
		}
		if r.RemoteType == "securityGroup" && slices.Contains(p.RemoteIn, r.RemoteSecurityGroup) {
			return true
		}
	}
	return false
}

func contains4(cidr, address string) bool {
	prefix, err := netip.ParsePrefix(cidr)
	addr, addrErr := netip.ParseAddr(address)
	return err == nil && addrErr == nil && prefix.Contains(addr)
}

func policyPortMatches(ports []networkingv1.NetworkPolicyPort, p Probe) bool {
	if len(ports) == 0 {
		return true
	}
	for _, port := range ports {
		protocol := "TCP"
		if port.Protocol != nil {
			protocol = string(*port.Protocol)
		}
		if !strings.EqualFold(protocol, p.Protocol) {
			continue
		}
		if port.Port == nil {
			return true
		}
		low := port.Port.IntValue()
		high := low
		if port.EndPort != nil {
			high = int(*port.EndPort)
		}
		if p.Port >= low && p.Port <= high {
			return true
		}
	}
	return false
}

func policyPeerMatches(peer networkingv1.NetworkPolicyPeer, p Probe) bool {
	if peer.IPBlock != nil {
		return contains4(peer.IPBlock.CIDR, p.Remote)
	}
	if peer.PodSelector != nil {
		for key, value := range peer.PodSelector.MatchLabels {
			member := false
			for _, group := range p.RemoteIn {
				member = member || (SecurityGroupLabel(group) == key && value == "true")
			}
			if !member {
				return false
			}
		}
		return len(peer.PodSelector.MatchLabels) > 0
	}
	return false
}

func AllowedByPolicy(np *networkingv1.NetworkPolicy, p Probe) bool {
	if p.Direction == "ingress" {
		for _, rule := range np.Spec.Ingress {
			if anyPeer(rule.From, p) && policyPortMatches(rule.Ports, p) {
				return true
			}
		}
		return false
	}
	for _, rule := range np.Spec.Egress {
		if anyPeer(rule.To, p) && policyPortMatches(rule.Ports, p) {
			return true
		}
	}
	return false
}

func anyPeer(peers []networkingv1.NetworkPolicyPeer, p Probe) bool {
	for _, peer := range peers {
		if policyPeerMatches(peer, p) {
			return true
		}
	}
	return false
}

func probeGrid() []Probe {
	var probes []Probe
	for _, direction := range []string{"ingress", "egress"} {
		for _, protocol := range []string{"TCP", "UDP", "ICMP"} {
			ports := []int{22, 80, 443, 8080}
			if protocol == "ICMP" {
				ports = []int{0}
			}
			for _, port := range ports {
				for _, remote := range []string{"10.1.2.3", "192.168.1.1", "8.8.8.8"} {
					for _, in := range [][]string{nil, {"web"}, {"db"}, {"web", "db"}} {
						probes = append(probes, Probe{Direction: direction, Protocol: protocol, Port: port, Remote: remote, RemoteIn: in})
					}
				}
			}
		}
	}
	return probes
}

func ruleSets() [][]v1alpha1.SecurityGroupRule {
	return [][]v1alpha1.SecurityGroupRule{
		nil,
		{tcpRule(443, "0.0.0.0/0")},
		{tcpRule(22, "10.0.0.0/8")},
		{{Protocol: "udp", Port: 80, CIDR: "10.1.2.0/24"}},
		{{Protocol: "tcp", Port: 80, EndPort: 8080, CIDR: "0.0.0.0/0"}},
		{{CIDR: "192.168.0.0/16"}},
		{{Protocol: "icmp", CIDR: "0.0.0.0/0"}},
		{{Protocol: "tcp", Port: 443, SecurityGroupRef: "web"}},
		{{SecurityGroupRef: "db"}},
		{{Protocol: "udp", SecurityGroupRef: "web"}},
		{tcpRule(22, "10.0.0.0/8"), tcpRule(80, "192.168.1.0/24"), {Protocol: "icmp", CIDR: "8.8.8.8/32"}},
		{{Protocol: "tcp", CIDR: "10.1.2.3/32"}},
		{{Protocol: "udp", Port: 22, EndPort: 443, CIDR: "10.0.0.0/8"}, {Protocol: "tcp", SecurityGroupRef: "db"}},
		{{CIDR: "0.0.0.0/0"}, tcpRule(22, "10.0.0.0/8")},
		{{Protocol: "icmp", SecurityGroupRef: "web"}},
		{tcpRule(8080, "8.8.8.8/32"), tcpRule(443, "8.8.8.8/32")},
		{{Protocol: "tcp", Port: 1, EndPort: 65535, CIDR: "192.168.0.0/16"}},
		{{CIDR: "10.1.2.3/32"}, {SecurityGroupRef: "web"}},
		{{Protocol: "udp", CIDR: "0.0.0.0/0"}},
		{{Protocol: "tcp", Port: 80, SecurityGroupRef: "db"}, {Protocol: "tcp", Port: 80, SecurityGroupRef: "web"}},
	}
}

func fabricProbeIn(p Probe) Probe {
	names := make([]string, len(p.RemoteIn))
	for i, g := range p.RemoteIn {
		names[i] = FabricName(sgNamespace, g)
	}
	p.RemoteIn = names
	return p
}

func lowerProtocol(p Probe) Probe {
	p.Protocol = strings.ToLower(p.Protocol)
	return p
}

func oracleFor(t *testing.T, set []v1alpha1.SecurityGroupRule, direction string) ([]fabric.SGRule, *networkingv1.NetworkPolicy) {
	t.Helper()
	rules := NormalizeRules(set)
	fabricSet, violation := FabricRules(sgNamespace, rules)
	if violation != nil {
		t.Fatalf("violation %+v for %+v", violation, set)
	}
	if direction == "ingress" {
		return fabricSet, PolicyFor(sgNamespace, "self", rules, nil, []string{"default"})
	}
	return EgressOrDefault(fabricSet), PolicyFor(sgNamespace, "self", nil, rules, []string{"default"})
}

func TestPolicyNeverAllowsMoreThanTheSecurityGroup(t *testing.T) {
	for index, set := range ruleSets() {
		for _, direction := range []string{"ingress", "egress"} {
			fabricSet, np := oracleFor(t, set, direction)
			for _, p := range probeGrid() {
				if p.Direction != direction {
					continue
				}
				if AllowedByPolicy(np, p) && !AllowedByFabric(fabricSet, lowerProtocol(fabricProbeIn(p))) {
					t.Fatalf("rule set %d %s: the policy allows %+v and the Fabric group does not", index, direction, p)
				}
			}
		}
	}
}

func TestPolicyMirrorsTheFabricGroupForTCPAndUDP(t *testing.T) {
	for index, set := range ruleSets() {
		for _, direction := range []string{"ingress", "egress"} {
			fabricSet, np := oracleFor(t, set, direction)
			for _, p := range probeGrid() {
				if p.Direction != direction || p.Protocol == "ICMP" {
					continue
				}
				if AllowedByPolicy(np, p) != AllowedByFabric(fabricSet, lowerProtocol(fabricProbeIn(p))) {
					t.Fatalf("rule set %d %s: the answers differ for %+v", index, direction, p)
				}
			}
		}
	}
}

func TestPolicyRendersPortRanges(t *testing.T) {
	np := PolicyFor(sgNamespace, "web", normalized(t, v1alpha1.SecurityGroupRule{Protocol: "tcp", Port: 80, EndPort: 90, CIDR: "10.0.0.0/8"}), nil, nil)
	port := np.Spec.Ingress[0].Ports[0]
	if port.Port == nil || *port.Port != intstr.FromInt32(80) || port.EndPort == nil || *port.EndPort != 90 || *port.Protocol != "TCP" {
		t.Fatalf("port %+v", port)
	}
	if np.Spec.Ingress[0].From[0].IPBlock == nil || np.Spec.Ingress[0].From[0].IPBlock.CIDR != "10.0.0.0/8" {
		t.Fatalf("peer %+v", np.Spec.Ingress[0].From)
	}
}

func TestPolicyOmitsICMPRules(t *testing.T) {
	np := PolicyFor(sgNamespace, "web", normalized(t, v1alpha1.SecurityGroupRule{Protocol: "icmp", CIDR: "10.0.0.0/8"}), nil, nil)
	if len(np.Spec.Ingress) != 0 {
		t.Fatalf("ingress %+v", np.Spec.Ingress)
	}
}
