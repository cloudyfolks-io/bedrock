package network

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func records(t *testing.T, zone string, in ...Record) []fabric.DNSRecord {
	t.Helper()
	got, v := FabricRecords(zone, in)
	if v != nil {
		t.Fatalf("violation %+v", v)
	}
	return got
}

func TestFabricRecordsRelativeAndApexNames(t *testing.T) {
	got := records(t, "internal.e2e", Record{Name: "db", IPs: []string{"10.0.0.5"}}, Record{Name: "@", IPs: []string{"10.0.0.6"}}, Record{Name: "api.internal.e2e", IPs: []string{"10.0.0.7"}})
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	if !slices.Equal(names, []string{"api.internal.e2e", "db.internal.e2e", "internal.e2e"}) {
		t.Fatalf("names %v", names)
	}
}

func TestFabricRecordsLowercaseAndTrailingDot(t *testing.T) {
	got := records(t, "internal.e2e", Record{Name: "DB", IPs: []string{"10.0.0.5"}}, Record{Name: "Web.Internal.E2E.", IPs: []string{"10.0.0.6"}})
	if got[0].Name != "db.internal.e2e" || got[1].Name != "web.internal.e2e" {
		t.Fatalf("records %+v", got)
	}
}

func TestFabricRecordsMergeDuplicateNames(t *testing.T) {
	got := records(t, "z.test", Record{Name: "db", IPs: []string{"10.0.0.9", "10.0.0.2"}}, Record{Name: "db.z.test", IPs: []string{"10.0.0.5", "10.0.0.2"}})
	if len(got) != 1 || !slices.Equal(got[0].IPs, []string{"10.0.0.2", "10.0.0.5", "10.0.0.9"}) {
		t.Fatalf("records %+v", got)
	}
}

func TestFabricRecordsRejectNamesOutsideTheZone(t *testing.T) {
	for _, name := range []string{"db.other.test", "db.other.test.", "a.b", "*", "bad_name", ""} {
		if _, v := FabricRecords("z.test", []Record{{Name: name, IPs: []string{"10.0.0.5"}}}); v == nil || v.Reason != ReasonInvalidRecords {
			t.Fatalf("name %q: %+v", name, v)
		}
	}
}

func TestFabricRecordsKeepIPv4AndIPv6TogetherInOneRecord(t *testing.T) {
	got := records(t, "z.test", Record{Name: "db", IPs: []string{"fd00::5", "10.0.0.5"}})
	if len(got) != 1 || len(got[0].IPs) != 2 {
		t.Fatalf("records %+v", got)
	}
}

func TestFabricRecordsRejectInvalidAddresses(t *testing.T) {
	for _, ip := range []string{"10.0.0", "x", "10.0.0.5/24", ""} {
		if _, v := FabricRecords("z.test", []Record{{Name: "db", IPs: []string{ip}}}); v == nil || v.Reason != ReasonInvalidRecords {
			t.Fatalf("ip %q: %+v", ip, v)
		}
	}
	if _, v := FabricRecords("z.test", []Record{{Name: "db"}}); v == nil {
		t.Fatal("a record without addresses must be refused")
	}
}

func TestFabricRecordsAcceptIPv6ForAAAA(t *testing.T) {
	got := records(t, "z.test", Record{Name: "db", IPs: []string{"2001:db8::1"}})
	if got[0].IPs[0] != "2001:db8::1" {
		t.Fatalf("records %+v", got)
	}
}

func subnetRef(namespace, name, network, zone string) v1alpha1.Subnet {
	return v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Spec: v1alpha1.SubnetSpec{NetworkRef: network, DNSZoneRef: zone}}
}

func TestNetworksOf(t *testing.T) {
	subnets := []v1alpha1.Subnet{
		subnetRef("a", "s1", "net2", "zone"), subnetRef("a", "s2", "net1", "zone"), subnetRef("a", "s3", "net1", "zone"),
		subnetRef("a", "s4", "net3", "other"), subnetRef("b", "s5", "net4", "zone"), subnetRef("a", "s6", "net5", ""),
	}
	if got := NetworksOf("a", "zone", subnets); !slices.Equal(got, []string{"net1", "net2"}) {
		t.Fatalf("networks %v", got)
	}
}
