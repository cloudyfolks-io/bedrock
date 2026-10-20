package network

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const fabricFieldManager = "fabric-controller"

func applyVpc(t *testing.T, c client.Client, name string, spec fabric.VpcSpec) {
	t.Helper()
	vpc := fabric.Vpc(fabric.Meta{Name: name}, spec)
	if err := ssa.Apply(context.Background(), c, vpc, v1alpha1.NetworkFieldManager); err != nil {
		t.Fatal(err)
	}
}

func coOwnVpc(t *testing.T, c client.Client, name string, spec fabric.VpcSpec) {
	t.Helper()
	vpc := fabric.Vpc(fabric.Meta{Name: name}, spec)
	if err := ssa.Apply(context.Background(), c, vpc, fabricFieldManager); err != nil {
		t.Fatal(err)
	}
}

func requireVpcFieldEmpty(t *testing.T, c client.Client, name string, field string) {
	t.Helper()
	value, found, _ := unstructured.NestedFieldNoCopy(vpcOf(t, c, name).Object, "spec", field)
	if list, isList := value.([]any); found && (!isList || len(list) > 0) {
		t.Fatalf("spec.%s is %v, want it empty", field, value)
	}
}

func TestVpcApplyDropsPeeringsWhenAnotherManagerOwnsTheField(t *testing.T) {
	c := cleanEnv(t)
	name := unique(t, "own")
	applyVpc(t, c, name, fabric.VpcSpec{Peerings: []fabric.Peering{{RemoteVpc: "other", LocalConnectIP: "169.254.0.1/30"}}})
	coOwnVpc(t, c, name, fabric.VpcSpec{Peerings: []fabric.Peering{{RemoteVpc: "other", LocalConnectIP: "169.254.0.1/30"}}})
	applyVpc(t, c, name, fabric.VpcSpec{})
	requireVpcFieldEmpty(t, c, name, "vpcPeerings")
}

func TestVpcApplyDropsStaticRoutesWhenAnotherManagerOwnsTheField(t *testing.T) {
	c := cleanEnv(t)
	name := unique(t, "own")
	applyVpc(t, c, name, fabric.VpcSpec{StaticRoutes: []fabric.StaticRoute{{CIDR: "10.2.0.0/16", NextHop: "169.254.0.2"}}})
	coOwnVpc(t, c, name, fabric.VpcSpec{StaticRoutes: []fabric.StaticRoute{{CIDR: "10.2.0.0/16", NextHop: "169.254.0.2"}}})
	applyVpc(t, c, name, fabric.VpcSpec{})
	requireVpcFieldEmpty(t, c, name, "staticRoutes")
}

func TestVpcApplyTurnsExternalOffWhenAnotherManagerOwnsTheField(t *testing.T) {
	c := cleanEnv(t)
	name := unique(t, "own")
	applyVpc(t, c, name, fabric.VpcSpec{EnableExternal: true, ExtraExternalSubnets: []string{"provider-public"}})
	coOwnVpc(t, c, name, fabric.VpcSpec{EnableExternal: true, ExtraExternalSubnets: []string{"provider-public"}})
	applyVpc(t, c, name, fabric.VpcSpec{})
	value, _, _ := unstructured.NestedBool(vpcOf(t, c, name).Object, "spec", "enableExternal")
	if value {
		t.Fatal("spec.enableExternal is still true")
	}
	requireVpcFieldEmpty(t, c, name, "extraExternalSubnets")
}
