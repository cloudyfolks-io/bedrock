package network

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const policyWait = 45 * time.Second

func splitDocuments(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(raw), "\n---\n")
}

func installPolicy(t *testing.T, c client.Client, path string, probe func() bool) {
	t.Helper()
	ctx := context.Background()
	docs := splitDocuments(t, path)
	var policy admissionregistrationv1.ValidatingAdmissionPolicy
	var binding admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if err := yaml.UnmarshalStrict([]byte(docs[0]), &policy); err != nil {
		t.Fatal(err)
	}
	if err := yaml.UnmarshalStrict([]byte(docs[1]), &binding); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Delete(ctx, &binding)
		_ = c.Delete(ctx, &policy)
		waitUntil(t, policyWait, func() bool { return !probe() })
	})
	waitUntil(t, policyWait, probe)
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

type subnetPolicyFixture struct {
	c      client.Client
	tenant string
	other  string
	p1, p2 string
	netA   string
	netB   string
	netC   string
}

func admitted(c client.Client, subnet *v1alpha1.Subnet) bool {
	err := c.Create(context.Background(), subnet)
	if err == nil {
		_ = c.Delete(context.Background(), subnet)
	}
	return err == nil
}

func newSubnetPolicyFixture(t *testing.T) subnetPolicyFixture {
	t.Helper()
	c, _ := testEnv(t)
	ctx := context.Background()
	f := subnetPolicyFixture{c: c, tenant: unique(t, "acme"), other: unique(t, "other")}
	f.p1 = newNamespace(t, f.tenant, "p1")
	f.p2 = newNamespace(t, f.tenant, "p2")
	f.netA, f.netB, f.netC = unique(t, "pola"), unique(t, "polb"), unique(t, "polc")
	for _, n := range []*v1alpha1.Network{networkOf(f.netA, f.tenant, "10.100.0.0/16"), networkOf(f.netB, f.tenant, "10.200.0.0/16"), networkOf(f.netC, f.other, "10.50.0.0/16")} {
		requireAccepted(t, c, n)
	}
	var a v1alpha1.Network
	if err := c.Get(ctx, client.ObjectKey{Name: f.netA}, &a); err != nil {
		t.Fatal(err)
	}
	a.Status.Subnets = []v1alpha1.NetworkSubnetStatus{{Namespace: f.p1, Name: "a", CIDR: "10.100.1.0/24"}}
	if err := c.Status().Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	probe := func() bool { return !admitted(c, policySubnet(f.p1, unique(t, "probe"), f.netA, "10.200.9.0/24")) }
	installPolicy(t, c, "../../manifests/90-bedrock/network-subnet-policy.yaml", probe)
	return f
}

func policySubnet(namespace, name, network, cidr string) *v1alpha1.Subnet {
	return &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: network, CIDR: cidr, DHCP: true},
	}
}

func requireDenied(t *testing.T, c client.Client, subnet *v1alpha1.Subnet, fragment string) {
	t.Helper()
	err := c.Create(context.Background(), subnet)
	if err == nil {
		t.Fatalf("Subnet %s %s must be denied", subnet.Spec.NetworkRef, subnet.Spec.CIDR)
	}
	if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("error %q must contain %q", err, fragment)
	}
}

func TestSubnetPolicyDeniesCIDRsOutsideTheNetwork(t *testing.T) {
	f := newSubnetPolicyFixture(t)
	requireDenied(t, f.c, policySubnet(f.p2, "outside", f.netA, "10.200.1.0/24"), "inside the cidr of its Network")
}

func TestSubnetPolicyDeniesOverlapWithStatusSubnets(t *testing.T) {
	f := newSubnetPolicyFixture(t)
	requireDenied(t, f.c, policySubnet(f.p2, "sub", f.netA, "10.100.1.128/25"), "overlaps another Subnet")
	requireDenied(t, f.c, policySubnet(f.p2, "super", f.netA, "10.100.0.0/20"), "overlaps another Subnet")
	requireAccepted(t, f.c, policySubnet(f.p2, "free", f.netA, "10.100.2.0/24"))
	itself := policySubnet(f.p1, "a", f.netA, "10.100.1.0/24")
	requireAccepted(t, f.c, itself)
	itself.Labels = map[string]string{"touch": "1"}
	if err := f.c.Update(context.Background(), itself); err != nil {
		t.Fatalf("an update of the Subnet itself must be admitted: %v", err)
	}
}

func TestSubnetPolicyAllowsOtherNetworks(t *testing.T) {
	f := newSubnetPolicyFixture(t)
	requireAccepted(t, f.c, policySubnet(f.p2, "other", f.netB, "10.200.1.0/24"))
}

func TestSubnetPolicyIgnoresUnknownNetwork(t *testing.T) {
	f := newSubnetPolicyFixture(t)
	requireAccepted(t, f.c, policySubnet(f.p2, "lost", "missing", "10.100.1.0/24"))
}

func TestSubnetPolicyDeniesAForeignTenantNetwork(t *testing.T) {
	f := newSubnetPolicyFixture(t)
	requireDenied(t, f.c, policySubnet(f.p1, "foreign", f.netC, "10.50.1.0/24"), "another tenant")
}
