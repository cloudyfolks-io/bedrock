package network

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	ruleIPVersion       = "ipv4"
	rulePriority        = 100
	rulePolicy          = "allow"
	remoteAddress       = "address"
	remoteSecurityGroup = "securityGroup"
	allAddresses        = "0.0.0.0/0"
	annotationPolicyFor = "fabric.cloudyfolks.io/network_policy_for"
	policyNamePrefix    = "bedrock-sg-"
	noSubnets           = "none"
	maxPort             = 65535
)

type Rule struct {
	CIDR          string
	SecurityGroup string
	Protocol      string
	PortMin       int
	PortMax       int
}

func NormalizeRules(spec []v1alpha1.SecurityGroupRule) []Rule {
	rules := make([]Rule, len(spec))
	for i, r := range spec {
		protocol := r.Protocol
		if protocol == "" {
			protocol = v1alpha1.SecurityGroupProtocolAll
		}
		rule := Rule{CIDR: r.CIDR, SecurityGroup: r.SecurityGroupRef, Protocol: protocol, PortMin: int(r.Port), PortMax: int(r.Port)}
		if r.EndPort != 0 {
			rule.PortMax = int(r.EndPort)
		}
		rules[i] = rule
	}
	return rules
}

func invalidRule(index int, format string, args ...any) *Violation {
	return &Violation{Reason: ReasonInvalidRules, Message: fmt.Sprintf("rule %d: %s", index+1, fmt.Sprintf(format, args...))}
}

func validateRule(index int, r Rule) *Violation {
	if (r.CIDR == "") == (r.SecurityGroup == "") {
		return invalidRule(index, "set exactly one of cidr and securityGroupRef")
	}
	if !slices.Contains([]string{v1alpha1.SecurityGroupProtocolAll, v1alpha1.SecurityGroupProtocolICMP, v1alpha1.SecurityGroupProtocolTCP, v1alpha1.SecurityGroupProtocolUDP}, r.Protocol) {
		return invalidRule(index, "the protocol %q is not supported", r.Protocol)
	}
	hasPorts := r.PortMin != 0 || r.PortMax != 0
	if hasPorts && r.Protocol != v1alpha1.SecurityGroupProtocolTCP && r.Protocol != v1alpha1.SecurityGroupProtocolUDP {
		return invalidRule(index, "ports need the tcp or udp protocol")
	}
	if hasPorts && (r.PortMin < 1 || r.PortMax > maxPort || r.PortMin > r.PortMax) {
		return invalidRule(index, "the ports must be from 1 to %d and ascending", maxPort)
	}
	if r.CIDR == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(r.CIDR)
	if err != nil {
		return invalidRule(index, "the cidr %q is not valid", r.CIDR)
	}
	if !prefix.Addr().Is4() {
		return &Violation{Reason: ReasonUnsupported, Message: fmt.Sprintf("rule %d: IPv6 is not supported; use an IPv4 cidr", index+1)}
	}
	return nil
}

func fabricRule(namespace string, r Rule) fabric.SGRule {
	rule := fabric.SGRule{IPVersion: ruleIPVersion, Protocol: r.Protocol, Priority: rulePriority, Policy: rulePolicy, PortMin: r.PortMin, PortMax: r.PortMax}
	if r.SecurityGroup != "" {
		rule.RemoteType, rule.RemoteSecurityGroup = remoteSecurityGroup, FabricName(namespace, r.SecurityGroup)
		return rule
	}
	rule.RemoteType, rule.RemoteAddress = remoteAddress, r.CIDR
	return rule
}

func FabricRules(namespace string, rules []Rule) ([]fabric.SGRule, *Violation) {
	out := make([]fabric.SGRule, len(rules))
	for i, r := range rules {
		if v := validateRule(i, r); v != nil {
			return nil, v
		}
		out[i] = fabricRule(namespace, r)
	}
	return out, nil
}

func EgressOrDefault(egress []fabric.SGRule) []fabric.SGRule {
	if len(egress) > 0 {
		return egress
	}
	return []fabric.SGRule{{IPVersion: ruleIPVersion, Protocol: v1alpha1.SecurityGroupProtocolAll, RemoteType: remoteAddress, RemoteAddress: allAddresses, Policy: rulePolicy, Priority: rulePriority}}
}

func policyPeer(r Rule) networkingv1.NetworkPolicyPeer {
	if r.SecurityGroup != "" {
		return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{SecurityGroupLabel(r.SecurityGroup): "true"}}}
	}
	return networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: r.CIDR}}
}

func policyPorts(r Rule) []networkingv1.NetworkPolicyPort {
	protocol := corev1.ProtocolTCP
	switch r.Protocol {
	case v1alpha1.SecurityGroupProtocolAll:
		return nil
	case v1alpha1.SecurityGroupProtocolUDP:
		protocol = corev1.ProtocolUDP
	}
	port := networkingv1.NetworkPolicyPort{Protocol: ptr.To(protocol)}
	if r.PortMin == 0 {
		return []networkingv1.NetworkPolicyPort{port}
	}
	port.Port = ptr.To(intstr.FromInt32(int32(r.PortMin)))
	if r.PortMax != r.PortMin {
		port.EndPort = ptr.To(int32(r.PortMax))
	}
	return []networkingv1.NetworkPolicyPort{port}
}

func expressible(rules []Rule) []Rule {
	return slices.DeleteFunc(slices.Clone(rules), func(r Rule) bool { return r.Protocol == v1alpha1.SecurityGroupProtocolICMP })
}

func ingressRules(rules []Rule) []networkingv1.NetworkPolicyIngressRule {
	var out []networkingv1.NetworkPolicyIngressRule
	for _, r := range expressible(rules) {
		out = append(out, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{policyPeer(r)}, Ports: policyPorts(r)})
	}
	return out
}

func egressRules(rules []Rule) []networkingv1.NetworkPolicyEgressRule {
	if len(rules) == 0 {
		return []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: allAddresses}}}}}
	}
	var out []networkingv1.NetworkPolicyEgressRule
	for _, r := range expressible(rules) {
		out = append(out, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{policyPeer(r)}, Ports: policyPorts(r)})
	}
	return out
}

func policyTargets(namespace string, subnets []string) string {
	if len(subnets) == 0 {
		return namespace + "/" + noSubnets
	}
	targets := make([]string, len(subnets))
	for i, subnet := range subnets {
		targets[i] = Attachment(namespace, subnet)
	}
	slices.Sort(targets)
	return strings.Join(targets, ",")
}

func PolicyFor(namespace, name string, ingress, egress []Rule, subnets []string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: policyNamePrefix + name, Annotations: map[string]string{annotationPolicyFor: policyTargets(namespace, subnets)}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{SecurityGroupLabel(name): "true"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     ingressRules(ingress),
			Egress:      egressRules(egress),
		},
	}
}

func DefaultSecurityGroup(namespace string, labels map[string]string) *unstructured.Unstructured {
	return fabric.SecurityGroup(fabric.Meta{Name: DefaultSecurityGroupName(namespace), Labels: labels}, nil, EgressOrDefault(nil))
}
