package operator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func validTenant(name string) *v1alpha1.Tenant {
	return &v1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.TenantSpec{
			DisplayName: "Acme",
			Owners:      []v1alpha1.SubjectRef{{Kind: v1alpha1.SubjectKindUser, Name: "alice"}},
			Quota:       v1alpha1.TenantQuota{CPU: ptr.To(resource.MustParse("64")), Memory: ptr.To(resource.MustParse("256Gi")), ElasticIPs: ptr.To(int32(4)), Projects: ptr.To(int32(10))},
			Network:     v1alpha1.TenantNetworkSpec{VPCCIDR: "10.100.0.0/16"},
		},
	}
}

func TestTenantValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	tenant := validTenant("acme")
	createValid(t, c, tenant)

	tenant.Status.VPC = "t-acme"
	tenant.Status.Usage.ElasticIPs = ptr.To(int32(1))
	if err := c.Status().Update(ctx, tenant); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	tenant.Spec.Network.VPCCIDR = "10.200.0.0/16"
	updateInvalid(t, c, tenant, "vpcCIDR is immutable")

	badCIDR := validTenant("bad-cidr")
	badCIDR.Spec.Network.VPCCIDR = "not-a-cidr"
	createInvalid(t, c, badCIDR, "vpcCIDR must be a CIDR")

	badOwner := validTenant("bad-owner")
	badOwner.Spec.Owners = []v1alpha1.SubjectRef{{Kind: "Robot", Name: "r2"}}
	createInvalid(t, c, badOwner, "Unsupported value")

	createInvalid(t, c, validTenant("Acme"), "the name must be lowercase letters and digits only")
	createInvalid(t, c, validTenant(strings.Repeat("a", 31)), "the name must have at most 30 characters")
	createValid(t, c, validTenant(strings.Repeat("a", 30)))
}

func TestProjectValidation(t *testing.T) {
	c, _ := StartTestEnv(t)

	project := &v1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: v1alpha1.ProjectSpec{
			TenantRef: "acme",
			Members:   []v1alpha1.ProjectMember{{Ref: v1alpha1.SubjectRef{Kind: v1alpha1.SubjectKindGroup, Name: "devs"}, Role: v1alpha1.MemberRoleEditor}},
			Quota:     v1alpha1.ProjectQuota{CPU: ptr.To(resource.MustParse("8")), VMs: ptr.To(int32(5))},
		},
	}
	createValid(t, c, project)

	project.Status.Namespace = v1alpha1.ProjectNamespace("acme", "shop")
	if err := c.Status().Update(context.Background(), project); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	project.Spec.TenantRef = "other"
	updateInvalid(t, c, project, "tenantRef is immutable")

	badRole := &v1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-role"},
		Spec: v1alpha1.ProjectSpec{
			TenantRef: "acme",
			Members:   []v1alpha1.ProjectMember{{Ref: v1alpha1.SubjectRef{Kind: v1alpha1.SubjectKindUser, Name: "bob"}, Role: "admin"}},
		},
	}
	createInvalid(t, c, badRole, "Unsupported value")

	badTenantRef := &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "bad-ref"}, Spec: v1alpha1.ProjectSpec{TenantRef: strings.Repeat("a", 31)}}
	createInvalid(t, c, badTenantRef, "spec.tenantRef")

	longName := &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 31)}, Spec: v1alpha1.ProjectSpec{TenantRef: "acme"}}
	createInvalid(t, c, longName, "the name must have at most 30 characters")
}

func TestNetworkValidation(t *testing.T) {
	c, _ := StartTestEnv(t)

	network := &v1alpha1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.DefaultNetworkName("acme")},
		Spec:       v1alpha1.NetworkSpec{TenantRef: "acme", CIDR: "10.100.0.0/16", EnableExternal: true},
	}
	createValid(t, c, network)

	network.Status.VPC = "t-acme"
	if err := c.Status().Update(context.Background(), network); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	network.Spec.CIDR = "10.101.0.0/16"
	updateInvalid(t, c, network, "cidr is immutable")

	var fresh v1alpha1.Network
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(network), &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.TenantRef = "other"
	updateInvalid(t, c, &fresh, "tenantRef is immutable")

	createInvalid(t, c, &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "bad"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDR: "10.0.0.0"}}, "cidr must be a CIDR")
}

func networkWith(name string, spec v1alpha1.NetworkSpec) *v1alpha1.Network {
	spec.TenantRef = "acme"
	return &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

func TestNetworkRangesValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	createValid(t, c, networkWith("ranges-old", v1alpha1.NetworkSpec{CIDR: "10.100.0.0/16"}))
	createValid(t, c, networkWith("ranges-several", v1alpha1.NetworkSpec{CIDRs: []string{"10.0.0.0/16", "172.16.0.0/16", "192.168.0.0/24"}}))
	createValid(t, c, networkWith("ranges-wide", v1alpha1.NetworkSpec{CIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}}))

	createInvalid(t, c, networkWith("ranges-both", v1alpha1.NetworkSpec{CIDR: "10.100.0.0/16", CIDRs: []string{"10.100.0.0/16"}}), "set exactly one of cidr and cidrs")
	createInvalid(t, c, networkWith("ranges-none", v1alpha1.NetworkSpec{}), "set exactly one of cidr and cidrs")
	createInvalid(t, c, networkWith("ranges-nested", v1alpha1.NetworkSpec{CIDRs: []string{"10.0.0.0/16", "10.0.1.0/24"}}), "the cidrs must not overlap each other")
	createInvalid(t, c, networkWith("ranges-nested-first", v1alpha1.NetworkSpec{CIDRs: []string{"10.0.1.0/24", "10.0.0.0/16"}}), "the cidrs must not overlap each other")
	createInvalid(t, c, networkWith("ranges-twice", v1alpha1.NetworkSpec{CIDRs: []string{"10.0.0.0/16", "10.0.0.0/16"}}), "Duplicate value")
	createInvalid(t, c, networkWith("ranges-host-bits", v1alpha1.NetworkSpec{CIDRs: []string{"10.0.0.5/16"}}), "every cidrs entry must be a canonical IPv4 network")
	createInvalid(t, c, networkWith("ranges-ipv6", v1alpha1.NetworkSpec{CIDRs: []string{"fd00::/64"}}), "every cidrs entry must be a canonical IPv4 network")
	createInvalid(t, c, networkWith("ranges-text", v1alpha1.NetworkSpec{CIDRs: []string{"nonsense"}}), "every cidrs entry must be a canonical IPv4 network")

	many := make([]string, 9)
	for i := range many {
		many[i] = fmt.Sprintf("10.%d.0.0/16", i)
	}
	createInvalid(t, c, networkWith("ranges-many", v1alpha1.NetworkSpec{CIDRs: many}), "must have at most 8 items")
}

func TestNetworkRangesMigration(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	old := networkWith("migrate-old", v1alpha1.NetworkSpec{CIDR: "10.100.0.0/16"})
	createValid(t, c, old)

	keepBoth := old.DeepCopy()
	keepBoth.Spec.CIDRs = []string{"10.100.0.0/16"}
	updateInvalid(t, c, keepBoth, "set exactly one of cidr and cidrs")

	drop := old.DeepCopy()
	drop.Spec.CIDR = ""
	drop.Spec.CIDRs = []string{"172.16.0.0/16"}
	updateInvalid(t, c, drop, "cidr can only be replaced by a cidrs list that holds it")

	change := old.DeepCopy()
	change.Spec.CIDR = "10.101.0.0/16"
	updateInvalid(t, c, change, "cidr is immutable")

	migrated := old.DeepCopy()
	migrated.Spec.CIDR = ""
	migrated.Spec.CIDRs = []string{"10.100.0.0/16", "172.16.0.0/16"}
	if err := c.Update(ctx, migrated); err != nil {
		t.Fatalf("replacing cidr by a list that holds it must be accepted: %v", err)
	}

	grown := migrated.DeepCopy()
	grown.Spec.CIDRs = append(grown.Spec.CIDRs, "192.168.0.0/24")
	if err := c.Update(ctx, grown); err != nil {
		t.Fatalf("adding a range must be accepted: %v", err)
	}

	shrunk := grown.DeepCopy()
	shrunk.Spec.CIDRs = []string{"10.100.0.0/16", "192.168.0.0/24"}
	updateInvalid(t, c, shrunk, "cidrs can only be added to")

	overlapping := grown.DeepCopy()
	overlapping.Spec.CIDRs = append(overlapping.Spec.CIDRs, "172.16.4.0/24")
	updateInvalid(t, c, overlapping, "the cidrs must not overlap each other")

	back := grown.DeepCopy()
	back.Spec.CIDRs = nil
	back.Spec.CIDR = "10.100.0.0/16"
	updateInvalid(t, c, back, "cidrs can only be added to")
}
