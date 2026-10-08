package compute

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func readySubnet(status metav1.ConditionStatus) v1alpha1.Subnet {
	subnet := v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-web", Name: "default"}}
	subnet.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: status}}
	return subnet
}

func TestAttachmentProvider(t *testing.T) {
	if got := AttachmentProvider("acme-web", "default"); got != "default.acme-web.fabric" {
		t.Fatal(got)
	}
}

func TestSubnetReady(t *testing.T) {
	if err := SubnetReady(readySubnet(metav1.ConditionTrue)); err != nil {
		t.Fatal(err)
	}
	if err := SubnetReady(readySubnet(metav1.ConditionFalse)); err != ErrSubnetNotReady {
		t.Fatal(err)
	}
	if err := SubnetReady(v1alpha1.Subnet{}); err != ErrSubnetNotReady {
		t.Fatal(err)
	}
}

func TestFabricName(t *testing.T) {
	got := FabricName("t1-prod", "default")
	if len(got) != 28 || !strings.HasPrefix(got, "t1-prod-default-") || got != FabricName("t1-prod", "default") {
		t.Fatal(got)
	}
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	for _, name := range []string{strings.Repeat("a", 63), strings.Repeat("a", 36) + "-" + strings.Repeat("b", 26)} {
		long := FabricName(strings.Repeat("n", 61), name)
		if len(long) > 63 || !label.MatchString(long) {
			t.Errorf("%q", long)
		}
	}
	if FabricName("x-y", "z-w") == FabricName("x-y-z", "w") {
		t.Fatal("the join must stay distinct")
	}
	if strings.Contains(FabricName("ns", "a.b"), ".") {
		t.Fatal("a dot must become a dash")
	}
}

func TestAttachmentsAreAlwaysNamespaced(t *testing.T) {
	got := Attachments([]NIC{{Namespace: "a", Subnet: "x"}, {Namespace: "acme-web", Subnet: "db"}})
	if !reflect.DeepEqual(got, []string{"a/x", "acme-web/db"}) {
		t.Fatal(got)
	}
}

func TestPodNetworkMetadata(t *testing.T) {
	nics := []NIC{
		{Namespace: "acme-web", Subnet: "default", MAC: "52:54:00:12:34:56", SecurityGroups: []string{"zeta", "alpha"}},
		{Namespace: "acme-web", Subnet: "db"},
	}
	annotations, labels := PodNetworkMetadata(nics)
	groups := []string{FabricName("acme-web", "alpha"), FabricName("acme-web", "zeta")}
	sort.Strings(groups)
	want := map[string]string{
		"default.acme-web.fabric.cloudyfolks.io/port_security":   "true",
		"default.acme-web.fabric.cloudyfolks.io/security_groups": strings.Join(groups, ","),
		"default.acme-web.fabric.cloudyfolks.io/mac_address":     "52:54:00:12:34:56",
		"db.acme-web.fabric.cloudyfolks.io/port_security":        "true",
		"db.acme-web.fabric.cloudyfolks.io/security_groups":      "default-acme-web",
		"db.acme-web.fabric.cloudyfolks.io/default_route":        "false",
	}
	if !reflect.DeepEqual(annotations, want) {
		t.Fatalf("annotations %+v", annotations)
	}
	if !reflect.DeepEqual(labels, map[string]string{"sg.bedrock.cloudyfolks.io/alpha": "true", "sg.bedrock.cloudyfolks.io/zeta": "true"}) {
		t.Fatalf("labels %+v", labels)
	}
}

func TestPodNetworkMetadataAlwaysEnablesPortSecurity(t *testing.T) {
	annotations, labels := PodNetworkMetadata([]NIC{
		{Namespace: "acme-web", Subnet: "a"},
		{Namespace: "acme-web", Subnet: "b", SecurityGroups: []string{"web"}},
	})
	for _, subnet := range []string{"a", "b"} {
		if annotations[subnet+".acme-web.fabric.cloudyfolks.io/port_security"] != "true" {
			t.Errorf("port security missing on %s", subnet)
		}
	}
	if len(labels) != 1 || labels["sg.bedrock.cloudyfolks.io/web"] != "true" {
		t.Fatalf("labels %+v", labels)
	}
}

func TestPodNetworkMetadataFallsBackToTheDefaultGroup(t *testing.T) {
	annotations, labels := PodNetworkMetadata([]NIC{{Namespace: "acme-web", Subnet: "default"}})
	if annotations["default.acme-web.fabric.cloudyfolks.io/security_groups"] != "default-acme-web" || len(labels) != 0 {
		t.Fatalf("annotations %+v labels %+v", annotations, labels)
	}
	if _, found := annotations["default.acme-web.fabric.cloudyfolks.io/default_route"]; found {
		t.Fatal("the first NIC carries the default route")
	}
}
