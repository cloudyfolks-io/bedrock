package network

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func facts(namespace, name, cidr string, age int) SubnetFacts {
	return SubnetFacts{Namespace: namespace, Name: name, CIDR: cidr, Created: epoch.Add(time.Duration(age) * time.Minute)}
}

func key(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

func reason(v *Violation) string {
	if v == nil {
		return ""
	}
	return v.Reason
}

func TestVerdictsAcceptNonOverlapping(t *testing.T) {
	verdicts := VerdictsFor("10.100.0.0/16", []SubnetFacts{facts("a", "x", "10.100.1.0/24", 1), facts("b", "y", "10.100.2.0/24", 2)})
	if len(verdicts) != 2 || verdicts[key("a", "x")] != nil || verdicts[key("b", "y")] != nil {
		t.Fatalf("unexpected verdicts %v", verdicts)
	}
}

func TestVerdictsRejectOutsideNetwork(t *testing.T) {
	verdicts := VerdictsFor("10.100.0.0/16", []SubnetFacts{facts("a", "x", "10.200.0.0/24", 1)})
	if reason(verdicts[key("a", "x")]) != ReasonCIDROutsideNetwork {
		t.Fatalf("unexpected verdicts %v", verdicts)
	}
}

func TestVerdictsRejectUnparsableCIDRs(t *testing.T) {
	verdicts := VerdictsFor("10.100.0.0/16", []SubnetFacts{facts("a", "x", "nope", 1)})
	if reason(verdicts[key("a", "x")]) != ReasonCIDROutsideNetwork {
		t.Fatalf("unexpected verdicts %v", verdicts)
	}
}

func TestVerdictsRejectOverlapKeepingTheOlder(t *testing.T) {
	cases := map[string][2]string{
		"equal":    {"10.100.1.0/24", "10.100.1.0/24"},
		"superset": {"10.100.0.0/20", "10.100.1.0/24"},
		"subset":   {"10.100.1.0/24", "10.100.0.0/20"},
	}
	for name, cidrs := range cases {
		t.Run(name, func(t *testing.T) {
			verdicts := VerdictsFor("10.100.0.0/16", []SubnetFacts{facts("b", "young", cidrs[1], 2), facts("a", "old", cidrs[0], 1)})
			if verdicts[key("a", "old")] != nil {
				t.Fatal("the older Subnet must win")
			}
			loser := verdicts[key("b", "young")]
			if loser == nil || loser.Reason != ReasonCIDROverlap || loser.Message == "" {
				t.Fatalf("unexpected verdict %v", loser)
			}
			if strings.Contains(loser.Message, "a/old") || strings.Contains(loser.Message, cidrs[0]) {
				t.Fatalf("message %q must not name the winner", loser.Message)
			}
		})
	}
}

func TestVerdictsTreatTheSameCIDRInTwoNamespacesAsOverlap(t *testing.T) {
	verdicts := VerdictsFor("10.100.0.0/16", []SubnetFacts{facts("a", "default", "10.100.1.0/24", 1), facts("b", "default", "10.100.1.0/24", 2)})
	if verdicts[key("a", "default")] != nil || reason(verdicts[key("b", "default")]) != ReasonCIDROverlap {
		t.Fatalf("unexpected verdicts %v", verdicts)
	}
}

func TestVerdictsAreIndependentOfListOrder(t *testing.T) {
	base := []SubnetFacts{
		facts("a", "one", "10.100.1.0/24", 1), facts("b", "two", "10.100.1.128/25", 2), facts("c", "three", "10.100.0.0/20", 3),
		facts("d", "four", "10.100.5.0/24", 4), facts("e", "five", "10.200.0.0/24", 5), facts("f", "six", "10.100.5.0/24", 6),
	}
	want := VerdictsFor("10.100.0.0/16", base)
	random := rand.New(rand.NewSource(7))
	for i := 0; i < 50; i++ {
		shuffled := append([]SubnetFacts(nil), base...)
		random.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := VerdictsFor("10.100.0.0/16", shuffled)
		for k, v := range want {
			if reason(got[k]) != reason(v) {
				t.Fatalf("shuffle %d: %v got %v want %v", i, k, got[k], v)
			}
		}
	}
}

func TestVerdictsTieOnEqualTimestampsUsesNamespaceThenName(t *testing.T) {
	verdicts := VerdictsFor("10.100.0.0/16", []SubnetFacts{facts("b", "a", "10.100.1.0/24", 1), facts("a", "z", "10.100.1.0/24", 1), facts("a", "b", "10.100.1.0/24", 1)})
	if verdicts[key("a", "b")] != nil || reason(verdicts[key("a", "z")]) != ReasonCIDROverlap || reason(verdicts[key("b", "a")]) != ReasonCIDROverlap {
		t.Fatalf("unexpected verdicts %v", verdicts)
	}
}

func TestAcceptedSubnetsSkipsRejected(t *testing.T) {
	subnets := []SubnetFacts{facts("b", "two", "10.100.2.0/24", 2), facts("a", "one", "10.100.1.0/24", 1), facts("c", "bad", "10.100.1.0/24", 3)}
	accepted := AcceptedSubnets(subnets, VerdictsFor("10.100.0.0/16", subnets))
	if len(accepted) != 2 || accepted[0].Name != "one" || accepted[1].Name != "two" {
		t.Fatalf("unexpected accepted %v", accepted)
	}
}

func TestDefaultGateway(t *testing.T) {
	for cidr, want := range map[string]string{"10.100.1.0/24": "10.100.1.1", "10.100.1.16/28": "10.100.1.17"} {
		got, err := DefaultGateway(cidr)
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", cidr, got, err)
		}
	}
	if _, err := DefaultGateway("nope"); err == nil {
		t.Fatal("invalid cidr must fail")
	}
}

func TestGatewayViolation(t *testing.T) {
	cases := map[string]struct {
		gateway string
		bad     bool
	}{
		"valid": {"10.100.1.1", false}, "last usable": {"10.100.1.254", false}, "network": {"10.100.1.0", true},
		"broadcast": {"10.100.1.255", true}, "outside": {"10.100.2.1", true}, "garbage": {"x", true}, "empty": {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v := GatewayViolation("10.100.1.0/24", tc.gateway)
			if (v != nil) != tc.bad || (v != nil && v.Reason != ReasonGatewayInvalid) {
				t.Fatalf("verdict %v", v)
			}
		})
	}
}

func TestCheckExcludeIPs(t *testing.T) {
	cases := map[string]struct {
		excludes []string
		bad      bool
	}{
		"none": {nil, false}, "single": {[]string{"10.100.1.5"}, false}, "range": {[]string{"10.100.1.2..10.100.1.9"}, false},
		"backwards": {[]string{"10.100.1.9..10.100.1.2"}, true}, "outside": {[]string{"10.100.2.5"}, true}, "range end outside": {[]string{"10.100.1.5..10.100.2.1"}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v := CheckExcludeIPs("10.100.1.0/24", tc.excludes)
			if (v != nil) != tc.bad || (v != nil && v.Reason != ReasonExcludeIPsInvalid) {
				t.Fatalf("verdict %v", v)
			}
		})
	}
}

func TestNetworkAllowed(t *testing.T) {
	network := v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme"}}
	if NetworkAllowed("acme", network) != nil {
		t.Fatal("same tenant must be allowed")
	}
	for _, label := range []string{"other", ""} {
		v := NetworkAllowed(label, network)
		if v == nil || v.Reason != ReasonNetworkNotOwned || v.Message != "the Network belongs to another tenant" {
			t.Fatalf("label %q: %v", label, v)
		}
	}
}
