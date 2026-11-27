package tenancy

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func vmWithPortSecurity(name string, portSecurity *bool) *v1alpha1.VirtualMachine {
	return &v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.VirtualMachineSpec{
			InstanceType: "small",
			RootDisk:     v1alpha1.VMRootDisk{SizeGi: 10},
			Networks:     []v1alpha1.VMNetwork{{SubnetRef: "default", PortSecurity: portSecurity}},
		},
	}
}

func portSecurityWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	return writerWorld(t, []string{"virtualmachines"}, "80-vm-port-security-policy.yaml", vmWithPortSecurity("probe", ptrBool(false)))
}

func ptrBool(v bool) *bool { return &v }

func TestNonAdminCannotCreateAVMWithPortSecurityOff(t *testing.T) {
	_, alice, _ := portSecurityWorld(t)
	err := alice.Create(context.Background(), vmWithPortSecurity("vpn", ptrBool(false)))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only an administrator may set or change portSecurity") {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminCanCreateAVMWithPortSecurityUnsetOrTrue(t *testing.T) {
	_, alice, _ := portSecurityWorld(t)
	mustCreate(t, alice, vmWithPortSecurity("plain", nil), vmWithPortSecurity("explicit", ptrBool(true)))
}

func TestAdminCanTurnPortSecurityOff(t *testing.T) {
	_, _, root := portSecurityWorld(t)
	mustCreate(t, root, vmWithPortSecurity("vpn", ptrBool(false)))
}

func TestNonAdminCannotChangePortSecurityOfAnExistingVM(t *testing.T) {
	admin, alice, _ := portSecurityWorld(t)
	ctx := context.Background()
	mustCreate(t, admin, vmWithPortSecurity("plain", nil), vmWithPortSecurity("vpn", ptrBool(false)))

	var plain v1alpha1.VirtualMachine
	if err := alice.Get(ctx, client.ObjectKey{Namespace: "default", Name: "plain"}, &plain); err != nil {
		t.Fatal(err)
	}
	plain.Spec.Networks[0].PortSecurity = ptrBool(false)
	if err := alice.Update(ctx, &plain); !apierrors.IsForbidden(err) {
		t.Fatalf("a tenant user must not turn it off: %v", err)
	}

	var vpn v1alpha1.VirtualMachine
	if err := alice.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpn"}, &vpn); err != nil {
		t.Fatal(err)
	}
	vpn.Spec.Networks[0].PortSecurity = nil
	if err := alice.Update(ctx, &vpn); !apierrors.IsForbidden(err) {
		t.Fatalf("a tenant user must not turn it on: %v", err)
	}
	vpn.Spec.Networks[0].PortSecurity = ptrBool(false)
	vpn.Spec.Running = false
	if err := alice.Update(ctx, &vpn); err != nil {
		t.Fatalf("a tenant user may edit other fields: %v", err)
	}
}
