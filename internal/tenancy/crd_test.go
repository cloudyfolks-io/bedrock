package tenancy

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func tenantWith(name, vpcCIDR string) *v1alpha1.Tenant {
	return &v1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.TenantSpec{
			Owners:  []v1alpha1.SubjectRef{{Kind: v1alpha1.SubjectKindUser, Name: "alice"}},
			Network: v1alpha1.TenantNetworkSpec{VPCCIDR: vpcCIDR},
		},
	}
}

func requireAccepted(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %s: %v", obj.GetName(), err)
	}
}

func requireRefused(t *testing.T, c client.Client, obj client.Object, message string) {
	t.Helper()
	err := c.Create(context.Background(), obj)
	if err == nil {
		t.Fatalf("create %s accepted, want %q", obj.GetName(), message)
	}
	if !strings.Contains(err.Error(), message) {
		t.Fatalf("create %s: %v, want %q", obj.GetName(), err, message)
	}
}

func TestTenantNameHasNoHyphen(t *testing.T) {
	c, _ := startEnv(t)
	requireAccepted(t, c, tenantWith("acme", "10.100.0.0/16"))
	requireAccepted(t, c, tenantWith("a1", "10.100.0.0/16"))
	requireRefused(t, c, tenantWith("a-b", "10.100.0.0/16"), "lowercase letters and digits")
	requireRefused(t, c, tenantWith(strings.Repeat("a", 31), "10.100.0.0/16"), "at most 30 characters")
}

func TestVPCCIDRLeavesRoomForASubnet(t *testing.T) {
	c, _ := startEnv(t)
	requireAccepted(t, c, tenantWith("cidra", "10.100.0.0/16"))
	requireAccepted(t, c, tenantWith("cidrb", "10.100.0.0/24"))
	requireRefused(t, c, tenantWith("cidrc", "10.100.0.0/25"), "prefix length of at most 24")
	requireRefused(t, c, tenantWith("cidrd", "fd00::/48"), "IPv4")
	requireRefused(t, c, tenantWith("cidre", "not-a-cidr"), "vpcCIDR must be a CIDR")
}

func TestTenantRefPatternHasNoHyphen(t *testing.T) {
	c, _ := startEnv(t)
	requireRefused(t, c, &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Spec: v1alpha1.ProjectSpec{TenantRef: "a-b"}}, "spec.tenantRef")
	requireRefused(t, c, &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "net"}, Spec: v1alpha1.NetworkSpec{TenantRef: "a-b", CIDR: "10.0.0.0/16"}}, "spec.tenantRef")
}
