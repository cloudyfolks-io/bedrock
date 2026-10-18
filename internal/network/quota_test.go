package network

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type quotaFixture struct {
	c      client.Client
	tenant string
	prod   string
	dev    string
}

func quotaWorld(t *testing.T, limit *int32) *quotaFixture {
	t.Helper()
	c, prod := poolWorld(t, []poolDef{{"a", "tenant"}}, map[string]capacity{"a": {0, 100}})
	var namespace corev1.Namespace
	if err := c.Get(context.Background(), client.ObjectKey{Name: prod}, &namespace); err != nil {
		t.Fatal(err)
	}
	tenant := namespace.Labels[v1alpha1.LabelTenant]
	requireAccepted(t, c, &v1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: tenant},
		Spec: v1alpha1.TenantSpec{
			Quota:   v1alpha1.TenantQuota{ElasticIPs: limit},
			Network: v1alpha1.TenantNetworkSpec{VPCCIDR: "10.100.0.0/16"},
		},
	})
	return &quotaFixture{c: c, tenant: tenant, prod: prod, dev: newNamespaceIn(t, c, tenant, "dev")}
}

func limitOf(n int32) *int32 { return &n }

func (f *quotaFixture) request(t *testing.T, namespace, name string) {
	t.Helper()
	unpinnedEIP(t, f.c, namespace, name)
}

func (f *quotaFixture) setLimit(t *testing.T, limit *int32) {
	t.Helper()
	var tenant v1alpha1.Tenant
	if err := f.c.Get(context.Background(), client.ObjectKey{Name: f.tenant}, &tenant); err != nil {
		t.Fatal(err)
	}
	tenant.Spec.Quota.ElasticIPs = limit
	if err := f.c.Update(context.Background(), &tenant); err != nil {
		t.Fatal(err)
	}
}

func requireProvisioned(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	if got := externalSubnetOf(t, c, namespace, name); got != "provider-a" {
		t.Fatalf("%s/%s external subnet %q, want provider-a", namespace, name, got)
	}
	if ready := meta.FindStatusCondition(getElasticIP(t, c, namespace, name).Status.Conditions, v1alpha1.ConditionReady); ready != nil && ready.Reason == ReasonQuotaExceeded {
		t.Fatalf("%s/%s must not wait for the quota", namespace, name)
	}
}

func requireWaitingForQuota(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	requireEIP(t, c, namespace, name, metav1.ConditionFalse, ReasonQuotaExceeded)
	requireNoFabricEip(t, c, namespace, name)
	if got := getElasticIP(t, c, namespace, name).Status.ProviderNetwork; got != "" {
		t.Fatalf("%s/%s took the pool %q while it waits", namespace, name, got)
	}
}

func TestElasticIPUnderTheQuotaProceeds(t *testing.T) {
	f := quotaWorld(t, limitOf(2))
	f.request(t, f.prod, "ip1")
	f.request(t, f.prod, "ip2")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	requireProvisioned(t, f.c, f.prod, "ip1")
	requireProvisioned(t, f.c, f.prod, "ip2")
}

func TestElasticIPAtTheQuotaWaits(t *testing.T) {
	f := quotaWorld(t, limitOf(1))
	f.request(t, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	f.request(t, f.prod, "ip2")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	requireProvisioned(t, f.c, f.prod, "ip1")
	requireWaitingForQuota(t, f.c, f.prod, "ip2")
}

func TestElasticIPsBeyondTheQuotaWaitAcrossProjects(t *testing.T) {
	f := quotaWorld(t, limitOf(1))
	f.request(t, f.dev, "ip1")
	f.request(t, f.prod, "ip2")
	f.request(t, f.dev, "ip3")
	reconcileElasticIP(t, f.c, f.dev, "ip3")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	requireWaitingForQuota(t, f.c, f.dev, "ip3")
	requireWaitingForQuota(t, f.c, f.prod, "ip2")
	reconcileElasticIP(t, f.c, f.dev, "ip1")
	requireProvisioned(t, f.c, f.dev, "ip1")
}

func TestElasticIPWithAPinnedPoolWaitsWithoutRecordingIt(t *testing.T) {
	f := quotaWorld(t, limitOf(1))
	f.request(t, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	pinned := elasticIPOf(f.prod, "ip2", "")
	pinned.Spec.ProviderNetwork = "a"
	requireAccepted(t, f.c, pinned)
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	requireWaitingForQuota(t, f.c, f.prod, "ip2")
}

func TestLoweredQuotaKeepsAProvisionedElasticIP(t *testing.T) {
	f := quotaWorld(t, limitOf(2))
	f.request(t, f.prod, "ip1")
	f.request(t, f.prod, "ip2")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	f.setLimit(t, limitOf(1))
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	requireProvisioned(t, f.c, f.prod, "ip1")
	requireProvisioned(t, f.c, f.prod, "ip2")
	f.request(t, f.prod, "ip3")
	reconcileElasticIP(t, f.c, f.prod, "ip3")
	requireWaitingForQuota(t, f.c, f.prod, "ip3")
}

func TestDeletingAnElasticIPLetsAWaitingOneProceed(t *testing.T) {
	f := quotaWorld(t, limitOf(1))
	f.request(t, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	f.request(t, f.dev, "ip2")
	reconcileElasticIP(t, f.c, f.dev, "ip2")
	requireWaitingForQuota(t, f.c, f.dev, "ip2")
	eip := getElasticIP(t, f.c, f.prod, "ip1")
	if err := f.c.Delete(context.Background(), &eip); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.dev, "ip2")
	requireProvisioned(t, f.c, f.dev, "ip2")
}

func TestRaisingTheQuotaLetsAWaitingElasticIPProceed(t *testing.T) {
	f := quotaWorld(t, limitOf(0))
	f.request(t, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	requireWaitingForQuota(t, f.c, f.prod, "ip1")
	f.setLimit(t, limitOf(1))
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	requireProvisioned(t, f.c, f.prod, "ip1")
}

func TestElasticIPsWithoutALimitAllProceed(t *testing.T) {
	f := quotaWorld(t, nil)
	for _, name := range []string{"ip1", "ip2", "ip3"} {
		f.request(t, f.prod, name)
		reconcileElasticIP(t, f.c, f.prod, name)
		requireProvisioned(t, f.c, f.prod, name)
	}
}

func TestElasticIPsOfATenantWithoutAnObjectAllProceed(t *testing.T) {
	c, ns := poolWorld(t, []poolDef{{"a", "tenant"}}, map[string]capacity{"a": {0, 100}})
	unpinnedEIP(t, c, ns, "ip1")
	reconcileElasticIP(t, c, ns, "ip1")
	requireProvisioned(t, c, ns, "ip1")
}

func TestQuotaWaitersAreTheElasticIPsWithoutAnAddressOrPool(t *testing.T) {
	f := quotaWorld(t, limitOf(1))
	f.request(t, f.prod, "ip1")
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	f.request(t, f.dev, "ip2")
	other := newNamespace(t, unique(t, "other"), "prod")
	f.request(t, other, "ip3")
	got := QuotaWaiters(context.Background(), f.c, f.tenant)
	if len(got) != 1 || got[0] != request(f.dev, "ip2") {
		t.Fatalf("requests %v", got)
	}
}
