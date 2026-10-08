package network

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func reconcileDNSZone(t *testing.T, c client.Client, namespace, name string, kinds KindProbe) reconcile.Result {
	t.Helper()
	result, err := (&DNSZoneReconciler{Client: c, Kinds: kinds}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireZone(t *testing.T, c client.Client, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var zone v1alpha1.DNSZone
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &zone); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, zone.Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func zoneFor(namespace, name string, records ...v1alpha1.DNSRecord) *v1alpha1.DNSZone {
	return &v1alpha1.DNSZone{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.DNSZoneSpec{Zone: "internal.e2e", Records: records},
	}
}

var dbRecord = v1alpha1.DNSRecord{Name: "db", IPs: []string{"10.100.1.5"}}

type zoneFixture struct {
	c         client.Client
	tenant    string
	namespace string
	networks  []v1alpha1.Network
}

func newZoneFixture(t *testing.T) zoneFixture {
	t.Helper()
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	f := zoneFixture{c: c, tenant: tenant, namespace: newNamespace(t, tenant, "prod")}
	for _, cidr := range []string{"10.100.0.0/16", "10.101.0.0/16"} {
		network := networkOf(unique(t, "net"), tenant, cidr)
		requireAccepted(t, c, network)
		reconcileNetwork(t, c, network.Name)
		f.networks = append(f.networks, *network)
	}
	return f
}

func (f zoneFixture) subnet(t *testing.T, name string, network int, cidr, zone string) {
	t.Helper()
	subnet := policySubnet(f.namespace, name, f.networks[network].Name, cidr)
	subnet.Spec.DNSZoneRef = zone
	requireAccepted(t, f.c, subnet)
}

func fabricZones(t *testing.T, c client.Client, name string) []*unstructured.Unstructured {
	t.Helper()
	zones, err := listObjects(context.Background(), c, fabric.DNSZoneGVK, client.MatchingLabels{v1alpha1.LabelKind: "DNSZone", v1alpha1.LabelName: name})
	if err != nil {
		t.Fatal(err)
	}
	return zones
}

func TestDNSZoneAppliesOneFabricZonePerNetwork(t *testing.T) {
	f := newZoneFixture(t)
	f.subnet(t, "one", 0, "10.100.1.0/24", "internal")
	f.subnet(t, "two", 1, "10.101.1.0/24", "internal")
	requireAccepted(t, f.c, zoneFor(f.namespace, "internal", dbRecord))
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	zones := fabricZones(t, f.c, "internal")
	if len(zones) != 2 {
		t.Fatalf("zones %d", len(zones))
	}
	vpcs := map[string]bool{}
	for _, z := range zones {
		vpc, _, _ := unstructured.NestedString(z.Object, "spec", "vpc")
		vpcs[vpc] = true
		records, _, _ := unstructured.NestedSlice(z.Object, "spec", "records")
		if len(records) != 1 || records[0].(map[string]any)["name"] != "db.internal.e2e" {
			t.Fatalf("records %v", records)
		}
	}
	if !vpcs[f.networks[0].Name] || !vpcs[f.networks[1].Name] {
		t.Fatalf("vpcs %v", vpcs)
	}
	requireZone(t, f.c, f.namespace, "internal", metav1.ConditionFalse, ReasonFabricNotReady)
	for _, z := range zones {
		if err := fabrictest.MarkDNSZoneReady(context.Background(), f.c, z.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	requireZone(t, f.c, f.namespace, "internal", metav1.ConditionTrue, ReasonProgrammed)
}

func TestDNSZoneWithoutSubnetsIsNotAttached(t *testing.T) {
	f := newZoneFixture(t)
	requireAccepted(t, f.c, zoneFor(f.namespace, "internal", dbRecord))
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	requireZone(t, f.c, f.namespace, "internal", metav1.ConditionFalse, ReasonNotAttached)
	if len(fabricZones(t, f.c, "internal")) != 0 {
		t.Fatal("no Fabric zone without a Subnet")
	}
}

func TestDNSZoneFollowsSubnetReferences(t *testing.T) {
	f := newZoneFixture(t)
	f.subnet(t, "one", 0, "10.100.1.0/24", "internal")
	f.subnet(t, "two", 1, "10.101.1.0/24", "internal")
	requireAccepted(t, f.c, zoneFor(f.namespace, "internal", dbRecord))
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	var two v1alpha1.Subnet
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.namespace, Name: "two"}, &two); err != nil {
		t.Fatal(err)
	}
	two.Spec.DNSZoneRef = ""
	if err := f.c.Update(context.Background(), &two); err != nil {
		t.Fatal(err)
	}
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	zones := fabricZones(t, f.c, "internal")
	if vpc, _, _ := unstructured.NestedString(zones[0].Object, "spec", "vpc"); len(zones) != 1 || vpc != f.networks[0].Name {
		t.Fatalf("zones %d", len(zones))
	}
}

func TestDNSZoneUnsupportedWithoutKind(t *testing.T) {
	f := newZoneFixture(t)
	f.subnet(t, "one", 0, "10.100.1.0/24", "internal")
	requireAccepted(t, f.c, zoneFor(f.namespace, "internal", dbRecord))
	reconcileDNSZone(t, f.c, f.namespace, "internal", func(schema.GroupKind) bool { return false })
	requireZone(t, f.c, f.namespace, "internal", metav1.ConditionFalse, ReasonUnsupported)
	if len(fabricZones(t, f.c, "internal")) != 0 {
		t.Fatal("nothing must be applied")
	}
}

func TestDNSZoneInvalidRecordsApplyNothing(t *testing.T) {
	f := newZoneFixture(t)
	f.subnet(t, "one", 0, "10.100.1.0/24", "internal")
	requireAccepted(t, f.c, zoneFor(f.namespace, "internal", v1alpha1.DNSRecord{Name: "db", IPs: []string{"not-an-ip"}}))
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	requireZone(t, f.c, f.namespace, "internal", metav1.ConditionFalse, ReasonInvalidRecords)
	if len(fabricZones(t, f.c, "internal")) != 0 {
		t.Fatal("nothing must be applied")
	}
}

func TestDNSZoneWaitsForTheNetworkVpc(t *testing.T) {
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	ns := newNamespace(t, tenant, "prod")
	requireAccepted(t, c, policySubnet(ns, "one", "missing-network", "10.100.1.0/24"))
	var subnet v1alpha1.Subnet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "one"}, &subnet); err != nil {
		t.Fatal(err)
	}
	subnet.Spec.DNSZoneRef = "internal"
	if err := c.Update(context.Background(), &subnet); err != nil {
		t.Fatal(err)
	}
	requireAccepted(t, c, zoneFor(ns, "internal", dbRecord))
	if result := reconcileDNSZone(t, c, ns, "internal", knowsEverything); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireZone(t, c, ns, "internal", metav1.ConditionFalse, ReasonFabricNotReady)
}

func TestDNSZoneDeletionRemovesFabricZones(t *testing.T) {
	f := newZoneFixture(t)
	f.subnet(t, "one", 0, "10.100.1.0/24", "internal")
	zone := zoneFor(f.namespace, "internal", dbRecord)
	requireAccepted(t, f.c, zone)
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	if err := f.c.Delete(context.Background(), zone); err != nil {
		t.Fatal(err)
	}
	reconcileDNSZone(t, f.c, f.namespace, "internal", knowsEverything)
	if len(fabricZones(t, f.c, "internal")) != 0 {
		t.Fatal("the Fabric zones must be deleted")
	}
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(zone), &v1alpha1.DNSZone{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}
