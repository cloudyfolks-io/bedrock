package network

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func underlayObjects(t *testing.T, c client.Client) map[string]*unstructured.Unstructured {
	t.Helper()
	out := map[string]*unstructured.Unstructured{}
	for _, gvk := range underlayKinds {
		items, err := listObjects(context.Background(), c, gvk, client.MatchingLabels{LabelComponent: ComponentUnderlay})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			out[item.GetKind()+"/"+item.GetName()] = item
		}
	}
	return out
}

func cleanUnderlay(t *testing.T, c client.Client) {
	t.Helper()
	t.Cleanup(func() {
		for _, o := range underlayObjects(t, c) {
			_ = c.Delete(context.Background(), o)
		}
	})
}

func TestUnderlayReconcilerAppliesAndPrunes(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	setSetting(t, "network.provider-networks", publicNetworks)
	r := &UnderlayReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), UnderlayRequest); err != nil {
		t.Fatal(err)
	}
	got := underlayObjects(t, c)
	if len(got) != 3 || got["Subnet/provider-public"] == nil {
		t.Fatalf("objects %d", len(got))
	}
	for _, o := range got {
		managed := false
		for _, entry := range o.GetManagedFields() {
			managed = managed || entry.Manager == v1alpha1.NetworkFieldManager
		}
		if !managed {
			t.Fatalf("%s is not managed by %s", o.GetKind(), v1alpha1.NetworkFieldManager)
		}
	}
	setSetting(t, "network.provider-networks", `[{"name":"backup","interface":"bond1","vlan":5,"cidr":"198.51.100.0/24","gateway":"198.51.100.1"}]`)
	if _, err := r.Reconcile(context.Background(), UnderlayRequest); err != nil {
		t.Fatal(err)
	}
	got = underlayObjects(t, c)
	if len(got) != 3 || got["Subnet/provider-backup"] == nil || got["Subnet/provider-public"] != nil || got["Vlan/public"] != nil {
		t.Fatalf("objects after change %v", len(got))
	}
}

func TestUnderlayReconcilerKeepsForeignObjects(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	foreign := fabric.Subnet(fabric.Meta{Name: unique(t, "foreign")}, fabric.SubnetSpec{CIDR: "10.250.0.0/24", Gateway: "10.250.0.1"})
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), foreign) })
	setSetting(t, "network.provider-networks", publicNetworks)
	if _, err := (&UnderlayReconciler{Client: c}).Reconcile(context.Background(), UnderlayRequest); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(foreign), foreign.DeepCopy()); err != nil {
		t.Fatalf("a foreign Subnet must survive: %v", err)
	}
}

func TestUnderlayReconcilerRendersNothingForEmptySettings(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	if _, err := (&UnderlayReconciler{Client: c}).Reconcile(context.Background(), UnderlayRequest); err != nil {
		t.Fatal(err)
	}
	if got := underlayObjects(t, c); len(got) != 0 {
		t.Fatalf("objects %d", len(got))
	}
}

func reconcileUnderlay(t *testing.T, c client.Client) {
	t.Helper()
	if _, err := (&UnderlayReconciler{Client: c}).Reconcile(context.Background(), UnderlayRequest); err != nil {
		t.Fatal(err)
	}
}

func bgpConfSpecIn(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	conf, err := getObject(context.Background(), c, fabric.BgpConfGVK, bgpConfName)
	if err != nil {
		t.Fatal(err)
	}
	spec, _, _ := unstructured.NestedMap(conf.Object, "spec")
	return spec
}

func TestUnderlayReconcilerAppliesTheBgpConf(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	for key, value := range bgpValues(nil) {
		setSetting(t, key, value)
	}
	reconcileUnderlay(t, c)
	spec := bgpConfSpecIn(t, c)
	if spec["localASN"] != int64(65001) || !reflect.DeepEqual(spec["nodeSelector"], map[string]any{fabric.LabelExternalGateway: "true"}) {
		t.Fatalf("spec %v", spec)
	}
}
