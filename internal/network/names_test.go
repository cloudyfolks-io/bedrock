package network

import (
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestFabricNameIsStable(t *testing.T) {
	got := FabricName("t1-prod", "default")
	if got != FabricName("t1-prod", "default") {
		t.Fatal("same input must give the same name")
	}
	if len(got) != 28 || !strings.HasPrefix(got, "t1-prod-default-") {
		t.Fatalf("unexpected name %q", got)
	}
}

func TestFabricNameFitsLabelValue(t *testing.T) {
	namespace := strings.Repeat("a", 61)
	name := strings.Repeat("b", 63)
	cut := strings.Repeat("a", 49) + "-" + strings.Repeat("b", 20)
	for _, tc := range [][2]string{{namespace, name}, {cut, "x"}, {strings.Repeat("a", 49), "-" + "x"}} {
		got := FabricName(tc[0], tc[1])
		if len(got) > 63 || !dnsLabel.MatchString(got) {
			t.Fatalf("name %q does not fit a label value", got)
		}
	}
}

func TestFabricNameDistinguishesTheJoinAmbiguity(t *testing.T) {
	if FabricName("x-y", "z-w") == FabricName("x-y-z", "w") {
		t.Fatal("different pairs must not collide")
	}
}

func TestFabricNameSanitizesDots(t *testing.T) {
	if strings.Contains(FabricName("ns", "a.b"), ".") {
		t.Fatal("dots must be replaced")
	}
}

func TestSubnetProvider(t *testing.T) {
	if got := SubnetProvider("t1-prod", "default"); got != "default.t1-prod.fabric" {
		t.Fatal(got)
	}
}

func TestAttachment(t *testing.T) {
	if got := Attachment("t1-prod", "default"); got != "t1-prod/default" {
		t.Fatal(got)
	}
}

func TestAttachmentConfig(t *testing.T) {
	want := `{"cniVersion":"0.3.0","type":"fabric","server_socket":"/run/openvswitch/fabric-daemon.sock","provider":"default.t1-prod.fabric"}`
	if got := AttachmentConfig("t1-prod", "default"); got != want {
		t.Fatal(got)
	}
}

func TestExternalSubnetName(t *testing.T) {
	if got := ExternalSubnetName("public"); got != "provider-public" {
		t.Fatal(got)
	}
}

func TestListenerRuleNameSeparatesPortsAndProtocols(t *testing.T) {
	names := map[string]bool{}
	for _, tc := range []struct {
		port     int32
		protocol string
	}{{80, "TCP"}, {80, "UDP"}, {443, "TCP"}} {
		names[ListenerRuleName("ns", "lb", tc.port, tc.protocol)] = true
	}
	if len(names) != 3 {
		t.Fatalf("rule names collide: %v", names)
	}
}

func TestListenerRuleNameLeavesRoomForTheServicePrefix(t *testing.T) {
	got := ListenerRuleName(strings.Repeat("a", 61), strings.Repeat("b", 63), 65535, "TCP")
	if len(got) > 59 {
		t.Fatalf("rule name %q is too long", got)
	}
}

func TestDNSZoneNameFitsLabelValue(t *testing.T) {
	got := DNSZoneName(strings.Repeat("a", 61), strings.Repeat("b", 63), strings.Repeat("c", 30))
	if len(got) > 63 || !dnsLabel.MatchString(got) {
		t.Fatalf("zone name %q does not fit", got)
	}
}

func TestSecurityGroupNames(t *testing.T) {
	if SecurityGroupLabel("web") != "sg.bedrock.cloudyfolks.io/web" || DefaultSecurityGroupName("t1-prod") != "default-t1-prod" {
		t.Fatal("unexpected security group names")
	}
}

func TestOwnerRoundTrip(t *testing.T) {
	owner := &metav1.ObjectMeta{Namespace: "ns", Name: "n", UID: "u1"}
	key, uid, ok := OwnerOf(&metav1.ObjectMeta{Annotations: OwnerAnnotations(owner)})
	if !ok || key != (types.NamespacedName{Namespace: "ns", Name: "n"}) || uid != "u1" {
		t.Fatalf("round trip failed: %v %v %v", key, uid, ok)
	}
}

func TestOwnerOfWithoutAnnotations(t *testing.T) {
	if _, _, ok := OwnerOf(&metav1.ObjectMeta{}); ok {
		t.Fatal("no annotations means no owner")
	}
}

func TestObjectLabelsOmitEmptyProject(t *testing.T) {
	labels := ObjectLabels("Subnet", "default", "t1", "")
	if _, found := labels[v1alpha1.LabelProject]; found {
		t.Fatal("empty project must not be labeled")
	}
	if labels[LabelManaged] != "true" || labels[v1alpha1.LabelTenant] != "t1" {
		t.Fatalf("unexpected labels %v", labels)
	}
}

func TestObjectLabelsWithoutTenant(t *testing.T) {
	labels := ObjectLabels("Vlan", "public", "", "")
	if _, found := labels[v1alpha1.LabelTenant]; found || len(labels) != 3 {
		t.Fatalf("unexpected labels %v", labels)
	}
}

func TestLBSubnetNameAppendsLBToTheNetwork(t *testing.T) {
	if got := LBSubnetName("acme"); got != "acme-lb" {
		t.Fatal(got)
	}
}

func TestLBSubnetNameFitsLabelValueForALongNetwork(t *testing.T) {
	long := strings.Repeat("a", 63)
	got := LBSubnetName(long)
	if len(got) > 63 || !dnsLabel.MatchString(got) || got == LBSubnetName(strings.Repeat("a", 62)+"b") {
		t.Fatalf("name %q", got)
	}
	if got != LBSubnetName(long) {
		t.Fatal("same input must give the same name")
	}
}

func TestInternalVipNameIsDistinctFromTheOldVipName(t *testing.T) {
	got := InternalVipName("t1-prod", "web")
	if got == FabricName("t1-prod", "web") || got == FabricName("t1-prod", "web-vip") || len(got) > 63 || !dnsLabel.MatchString(got) {
		t.Fatalf("name %q", got)
	}
	if got != InternalVipName("t1-prod", "web") || got == InternalVipName("t1-prod", "web2") {
		t.Fatal("the name must be stable and unique per LoadBalancer")
	}
}
