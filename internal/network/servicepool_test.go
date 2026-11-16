package network

import (
	"context"
	"reflect"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func TestServicesLoadBalancerPoolUsesTheSelectedServicesSubnet(t *testing.T) {
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	pool := ServicesLoadBalancerPool(cfg)
	if pool == nil || pool.GetName() != "services-plane" || pool.GetKind() != "LoadBalancerPool" {
		t.Fatalf("pool %v", pool)
	}
	want := map[string]any{"subnet": "provider-services", "announce": "l2", "default": false}
	if !reflect.DeepEqual(pool.Object["spec"], want) {
		t.Fatalf("spec %v", pool.Object["spec"])
	}
	if empty := ServicesLoadBalancerPool(Config{}); empty != nil {
		t.Fatal("an unset services provider must not create a pool")
	}
}

func TestServicesPoolSyncPreservesTheDefaultVpcAndIsIdempotent(t *testing.T) {
	c := cleanEnv(t)
	ctx := context.Background()
	vpc := fabric.Vpc(fabric.Meta{Name: tenantManagementVPC}, fabric.VpcSpec{
		ExtraExternalSubnets: []string{"public"},
		StaticRoutes:         []fabric.StaticRoute{{CIDR: "192.0.2.0/24", NextHop: "10.0.0.1"}},
	})
	if err := c.Create(ctx, vpc); err != nil {
		t.Fatal(err)
	}
	routes, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	r := &UnderlayReconciler{Client: c}
	if ready, err := r.syncServicesPool(ctx, cfg); err != nil || !ready {
		t.Fatalf("ready %t err %v", ready, err)
	}
	updated, err := getObject(ctx, c, fabric.VpcGVK, tenantManagementVPC)
	if err != nil {
		t.Fatal(err)
	}
	enabled, _, _ := unstructured.NestedBool(updated.Object, "spec", "enableExternal")
	if !enabled || !slices.Equal(fabric.VpcSpecExternalSubnets(updated), []string{"public", "provider-services"}) {
		t.Fatalf("spec %v", updated.Object["spec"])
	}
	gotRoutes, _, _ := unstructured.NestedSlice(updated.Object, "spec", "staticRoutes")
	if !reflect.DeepEqual(gotRoutes, routes) {
		t.Fatalf("routes changed from %v to %v", routes, gotRoutes)
	}
	pool, err := getObject(ctx, c, fabric.LoadBalancerPoolGVK, servicesLoadBalancerPoolName)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := r.syncServicesPool(ctx, cfg); err != nil || !ready {
		t.Fatalf("second sync ready %t err %v", ready, err)
	}
	for _, before := range []*unstructured.Unstructured{updated, pool} {
		after := before.DeepCopy()
		if err := c.Get(ctx, client.ObjectKeyFromObject(before), after); err != nil {
			t.Fatal(err)
		}
		if after.GetResourceVersion() != before.GetResourceVersion() {
			t.Fatalf("%s changed on the second sync", before.GetKind())
		}
	}
}

func TestServicesPoolSyncRefusesForeignAndChangedPools(t *testing.T) {
	for _, change := range []string{"foreign", "subnet", "announce", "default", "selector"} {
		t.Run(change, func(t *testing.T) {
			c := cleanEnv(t)
			ctx := context.Background()
			cfg := configOf(t, servicesValues(servicesL2Pools, nil))
			pool := ServicesLoadBalancerPool(cfg)
			spec := pool.Object["spec"].(map[string]any)
			switch change {
			case "foreign":
				pool.SetLabels(nil)
			case "subnet":
				spec["subnet"] = "legacy-services"
			case "announce":
				spec["announce"] = "bgp"
			case "default":
				spec["default"] = true
			case "selector":
				spec["serviceSelector"] = map[string]any{"matchLabels": map[string]any{"app": "foreign"}}
			}
			if err := c.Create(ctx, pool); err != nil {
				t.Fatal(err)
			}
			before := pool.DeepCopy()
			if _, err := (&UnderlayReconciler{Client: c}).syncServicesPool(ctx, cfg); err == nil {
				t.Fatal("an existing pool must not be adopted or reconfigured")
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(pool), pool); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pool.Object, before.Object) {
				t.Fatal("the existing pool changed")
			}
		})
	}
}

func TestServicesPoolSyncWaitsForFabricToCreateTheDefaultVpc(t *testing.T) {
	c := cleanEnv(t)
	ctx := context.Background()
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	if ready, err := (&UnderlayReconciler{Client: c}).syncServicesPool(ctx, cfg); err != nil || ready {
		t.Fatalf("ready %t err %v", ready, err)
	}
	if _, err := getObject(ctx, c, fabric.VpcGVK, tenantManagementVPC); !apierrors.IsNotFound(err) {
		t.Fatalf("the operator must not create the default Vpc: %v", err)
	}
}

func TestServicesPoolKeepsItsSubnetWhenConfigurationIsRemoved(t *testing.T) {
	c := cleanEnv(t)
	ctx := context.Background()
	cfg := configOf(t, servicesValues(servicesL2Pools, nil))
	pool := ServicesLoadBalancerPool(cfg)
	subnet := fabric.Subnet(underlayMeta("Subnet", "provider-services"), fabric.SubnetSpec{CIDR: "100.66.0.0/15", Gateway: "100.66.0.1"})
	for _, obj := range []*unstructured.Unstructured{pool, subnet} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	r := &UnderlayReconciler{Client: c}
	if ready, err := r.syncServicesPool(ctx, Config{}); err != nil || !ready {
		t.Fatalf("ready %t err %v", ready, err)
	}
	if refused, err := r.prune(ctx, map[string]bool{}); err != nil || refused == "" {
		t.Fatalf("prune must refuse an attached subnet: %q %v", refused, err)
	}
	for _, obj := range []*unstructured.Unstructured{pool, subnet} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatalf("%s must survive: %v", obj.GetKind(), err)
		}
	}
}
