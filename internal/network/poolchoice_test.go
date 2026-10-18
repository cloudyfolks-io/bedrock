package network

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

type poolDef struct {
	name string
	role string
}

type capacity struct{ using, available int64 }

func poolList(pools []poolDef) string {
	entries := make([]string, len(pools))
	for i, p := range pools {
		entry := fmt.Sprintf(`{"name":"%s","interface":"bond0","vlan":%d,"cidr":"198.51.%d.0/24","gateway":"198.51.%d.1"`, p.name, 100+i, 100+i, 100+i)
		if p.role != "" {
			entry += fmt.Sprintf(`,"role":"%s"`, p.role)
		}
		entries[i] = entry + "}"
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func setUsage(t *testing.T, c client.Client, usage map[string]capacity) {
	t.Helper()
	for name, u := range usage {
		if err := fabrictest.SetSubnetCapacity(context.Background(), c, ExternalSubnetName(name), u.using, u.available); err != nil {
			t.Fatal(err)
		}
	}
}

func poolWorld(t *testing.T, pools []poolDef, usage map[string]capacity) (client.Client, string) {
	t.Helper()
	c := cleanEnv(t)
	setSetting(t, "network.provider-networks", poolList(pools))
	if _, err := (&UnderlayReconciler{Client: c}).Reconcile(context.Background(), UnderlayRequest); err != nil {
		t.Fatal(err)
	}
	setUsage(t, c, usage)
	return c, newNamespace(t, unique(t, "acme"), "prod")
}

func unpinnedEIP(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	eip := elasticIPOf(namespace, name, "")
	eip.Spec.ProviderNetwork = ""
	requireAccepted(t, c, eip)
}

func externalSubnetOf(t *testing.T, c client.Client, namespace, name string) string {
	t.Helper()
	eip, err := eipObject(c, namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	subnet, _, _ := unstructured.NestedString(eip.Object, "spec", "externalSubnet")
	return subnet
}

func requireNoFabricEip(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	if _, err := eipObject(c, namespace, name); !apierrors.IsNotFound(err) {
		t.Fatalf("no Fabric object expected: %v", err)
	}
}

func TestElasticIPWithoutAPoolTakesTheLeastUsedTenantPool(t *testing.T) {
	pools := []poolDef{{"a", ""}, {"b", "tenant"}, {"c", "tenant"}, {"plat", "platform"}}
	c, ns := poolWorld(t, pools, map[string]capacity{"a": {50, 50}, "b": {10, 90}, "c": {30, 70}, "plat": {0, 100}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	if got := externalSubnetOf(t, c, ns, "ip1"); got != "provider-b" {
		t.Fatalf("external subnet %q", got)
	}
	if got := getElasticIP(t, c, ns, "ip1").Status.ProviderNetwork; got != "b" {
		t.Fatalf("status pool %q", got)
	}
}

func TestElasticIPPoolChoiceBreaksATieByName(t *testing.T) {
	pools := []poolDef{{"a", ""}, {"b", "tenant"}, {"c", "tenant"}}
	c, ns := poolWorld(t, pools, map[string]capacity{"a": {50, 50}, "b": {20, 180}, "c": {10, 90}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	if got := getElasticIP(t, c, ns, "ip1").Status.ProviderNetwork; got != "b" {
		t.Fatalf("status pool %q, want b", got)
	}
}

func TestElasticIPPoolChoiceSkipsAFullPool(t *testing.T) {
	pools := []poolDef{{"a", ""}, {"b", "tenant"}}
	c, ns := poolWorld(t, pools, map[string]capacity{"a": {100, 0}, "b": {90, 10}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	if got := getElasticIP(t, c, ns, "ip1").Status.ProviderNetwork; got != "b" {
		t.Fatalf("status pool %q, want b", got)
	}
}

func TestElasticIPWithEveryTenantPoolFullStopsBeforeFabric(t *testing.T) {
	pools := []poolDef{{"a", ""}, {"b", "tenant"}, {"plat", "platform"}}
	c, ns := poolWorld(t, pools, map[string]capacity{"a": {100, 0}, "b": {100, 0}, "plat": {0, 100}})
	unpinnedEIP(t, c, ns, "ip1")
	if result := reconcileElasticIP(t, c, ns, "ip1"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireEIP(t, c, ns, "ip1", metav1.ConditionFalse, ReasonNoPoolCapacity)
	requireNoFabricEip(t, c, ns, "ip1")
	if got := getElasticIP(t, c, ns, "ip1").Status.ProviderNetwork; got != "" {
		t.Fatalf("status pool %q", got)
	}
}

func TestElasticIPWithNoTenantPoolStopsBeforeFabric(t *testing.T) {
	c, ns := poolWorld(t, []poolDef{{"plat", "platform"}}, map[string]capacity{"plat": {0, 100}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	requireEIP(t, c, ns, "ip1", metav1.ConditionFalse, ReasonNoPoolCapacity)
	requireNoFabricEip(t, c, ns, "ip1")
}

func TestElasticIPTakesAPoolWhenCapacityReturns(t *testing.T) {
	c, ns := poolWorld(t, []poolDef{{"a", ""}}, map[string]capacity{"a": {100, 0}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	requireEIP(t, c, ns, "ip1", metav1.ConditionFalse, ReasonNoPoolCapacity)
	setUsage(t, c, map[string]capacity{"a": {99, 1}})
	reconcileElasticIP(t, c, ns, "ip1")
	if got := externalSubnetOf(t, c, ns, "ip1"); got != "provider-a" {
		t.Fatalf("external subnet %q", got)
	}
}

func TestElasticIPKeepsItsChosenPool(t *testing.T) {
	pools := []poolDef{{"a", ""}, {"b", "tenant"}}
	c, ns := poolWorld(t, pools, map[string]capacity{"a": {50, 50}, "b": {10, 90}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	setUsage(t, c, map[string]capacity{"a": {0, 100}, "b": {100, 0}})
	reconcileElasticIP(t, c, ns, "ip1")
	if err := fabrictest.MarkEipAllocated(context.Background(), c, FabricName(ns, "ip1"), "198.51.101.20"); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, c, ns, "ip1")
	got := getElasticIP(t, c, ns, "ip1")
	if got.Status.ProviderNetwork != "b" || got.Status.Address != "198.51.101.20" || got.Spec.ProviderNetwork != "" {
		t.Fatalf("status %+v spec %+v", got.Status, got.Spec)
	}
	if subnet := externalSubnetOf(t, c, ns, "ip1"); subnet != "provider-b" {
		t.Fatalf("external subnet %q", subnet)
	}
}

func TestElasticIPNamedPoolIsReportedInTheStatus(t *testing.T) {
	pools := []poolDef{{"a", ""}, {"plat", "platform"}}
	c, ns := poolWorld(t, pools, map[string]capacity{"a": {0, 100}, "plat": {100, 0}})
	eip := elasticIPOf(ns, "ip1", "")
	eip.Spec.ProviderNetwork = "plat"
	requireAccepted(t, c, eip)
	reconcileElasticIP(t, c, ns, "ip1")
	if got := getElasticIP(t, c, ns, "ip1").Status.ProviderNetwork; got != "plat" {
		t.Fatalf("status pool %q", got)
	}
	if subnet := externalSubnetOf(t, c, ns, "ip1"); subnet != "provider-plat" {
		t.Fatalf("external subnet %q", subnet)
	}
}
