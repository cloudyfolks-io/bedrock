package network

import (
	"context"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func nicSubnet(gateway string, excludes ...string) v1alpha1.Subnet {
	return v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: "10.100.0.0/24", Gateway: gateway, ExcludeIPs: excludes}}
}

func TestCheckNICAddress(t *testing.T) {
	cases := map[string]struct {
		subnet  v1alpha1.Subnet
		address string
		want    string
	}{
		"free":                       {nicSubnet(""), "10.100.0.50", ""},
		"first usable":               {nicSubnet(""), "10.100.0.2", ""},
		"outside":                    {nicSubnet(""), "10.101.0.50", ReasonAddressOutsideSubnet},
		"not an address":             {nicSubnet(""), "nonsense", ReasonAddressOutsideSubnet},
		"default gateway":            {nicSubnet(""), "10.100.0.1", ReasonAddressReserved},
		"named gateway":              {nicSubnet("10.100.0.254"), "10.100.0.254", ReasonAddressReserved},
		"first with a named gateway": {nicSubnet("10.100.0.254"), "10.100.0.1", ""},
		"network address":            {nicSubnet(""), "10.100.0.0", ReasonAddressReserved},
		"broadcast address":          {nicSubnet(""), "10.100.0.255", ReasonAddressReserved},
		"excluded address":           {nicSubnet("", "10.100.0.20"), "10.100.0.20", ReasonAddressReserved},
		"excluded range start":       {nicSubnet("", "10.100.0.20..10.100.0.30"), "10.100.0.20", ReasonAddressReserved},
		"excluded range inside":      {nicSubnet("", "10.100.0.20..10.100.0.30"), "10.100.0.25", ReasonAddressReserved},
		"excluded range end":         {nicSubnet("", "10.100.0.20..10.100.0.30"), "10.100.0.30", ReasonAddressReserved},
		"after excluded range":       {nicSubnet("", "10.100.0.20..10.100.0.30"), "10.100.0.31", ""},
	}
	for label, tc := range cases {
		got := CheckNICAddress(tc.subnet, tc.address)
		if (got == nil) != (tc.want == "") || (got != nil && got.Reason != tc.want) {
			t.Errorf("%s: got %+v want %q", label, got, tc.want)
		}
	}
}

func fabricIP(name, namespace, pod, address string) *unstructured.Unstructured {
	ip := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"podName": pod, "namespace": namespace, "v4IpAddress": address}}}
	ip.SetGroupVersionKind(fabric.IPGVK)
	ip.SetName(name)
	return ip
}

func TestAddressHeldByAnother(t *testing.T) {
	ips := []*unstructured.Unstructured{
		fabricIP("a", "t1-prod", "web-1", "10.100.0.10"),
		fabricIP("b", "t1-prod", "db-1", "10.100.0.11"),
		fabricIP("c", "other", "web-1", "10.100.0.12"),
	}
	cases := map[string]struct {
		address string
		held    bool
	}{
		"free":                 {"10.100.0.50", false},
		"own address":          {"10.100.0.10", false},
		"another pod":          {"10.100.0.11", true},
		"same name other ns":   {"10.100.0.12", true},
		"a partial text match": {"10.100.0.1", false},
	}
	for label, tc := range cases {
		if got := AddressHeldByAnother(ips, tc.address, "t1-prod", "web-1"); got != tc.held {
			t.Errorf("%s: got %v want %v", label, got, tc.held)
		}
	}
}

type recordedIndexer struct {
	field   string
	extract client.IndexerFunc
}

func (r *recordedIndexer) IndexField(_ context.Context, _ client.Object, field string, extract client.IndexerFunc) error {
	r.field, r.extract = field, extract
	return nil
}

func TestFabricIPIndexKeysTheSubnetAndTheAddress(t *testing.T) {
	var indexer recordedIndexer
	if err := IndexFabricIPs(context.Background(), &indexer); err != nil {
		t.Fatal(err)
	}
	ip := fabricIP("a", "t1-prod", "web-1", "10.100.0.50")
	ip.SetLabels(map[string]string{labelFabricSubnet: "s-1"})
	if got := indexer.extract(ip); !slices.Equal(got, []string{"s-1/10.100.0.50"}) {
		t.Fatalf("keys %v", got)
	}
	if got := indexer.extract(fabricIP("b", "t1-prod", "mac-only", "")); got != nil {
		t.Fatalf("an IP object with no address has no key: %v", got)
	}
}

type listRecorder struct {
	client.Reader
	options client.ListOptions
}

func (l *listRecorder) List(_ context.Context, _ client.ObjectList, opts ...client.ListOption) error {
	l.options.ApplyOptions(opts)
	return nil
}

func TestIPsOfAddressLooksUpTheIndex(t *testing.T) {
	var indexer recordedIndexer
	if err := IndexFabricIPs(context.Background(), &indexer); err != nil {
		t.Fatal(err)
	}
	var reader listRecorder
	if _, err := IPsOfAddress(context.Background(), &reader, "s-1", "10.100.0.50"); err != nil {
		t.Fatal(err)
	}
	if value, exact := reader.options.FieldSelector.RequiresExactMatch(indexer.field); !exact || value != "s-1/10.100.0.50" {
		t.Fatalf("selector %v", reader.options.FieldSelector)
	}
}
