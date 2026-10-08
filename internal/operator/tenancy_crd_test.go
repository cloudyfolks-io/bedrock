package operator

import (
	"context"
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
