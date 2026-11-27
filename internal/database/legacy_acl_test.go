package database

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func TestManagedLegacyProxyRequiresCurrentFabricACLReceipt(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db := v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "acl-test", Namespace: "legacy-acl-test", UID: uuid.NewUUID()}}
	subnet := v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: "10.50.0.0/24"}}
	desired := SecurityGroupObject(db, Project{Namespace: db.Namespace, Tenant: "acme", Name: "web"}, subnet, "", 5432)
	group := desired.DeepCopy()
	group.UID = uuid.NewUUID()
	group.Generation = 4
	group.Status.ObservedGeneration = 4
	group.Status.Conditions = []metav1.Condition{
		{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 4},
	}
	ingress, failure := network.FabricRules(group.Namespace, network.NormalizeRules(group.Spec.Ingress))
	if failure != nil {
		t.Fatalf("render ingress: %v", failure)
	}
	egress, failure := network.FabricRules(group.Namespace, network.NormalizeRules(group.Spec.Egress))
	if failure != nil {
		t.Fatalf("render egress: %v", failure)
	}
	child := fabric.SecurityGroup(fabric.Meta{Name: network.FabricName(group.Namespace, group.Name), Annotations: network.OwnerAnnotations(group)}, ingress, egress)
	must(t, c.Create(ctx, child))
	receipt := map[string]any{"portGroup": "legacy-proxy-pg", "ingressLastSyncSuccess": true, "egressLastSyncSuccess": true}
	r := &Reconciler{Live: c}
	check := func(name string, actual, expected *v1alpha1.SecurityGroup, want bool) {
		t.Helper()
		ready, err := r.legacySecurityGroupReady(ctx, db, actual, expected)
		if err != nil || ready != want {
			t.Fatalf("%s readiness %v with error %v, want %v with no error", name, ready, err, want)
		}
	}
	child.Object["status"] = receipt
	must(t, c.Status().Update(ctx, child))
	check("legacy status without a receipt", group, desired, false)
	receipt["observedGeneration"] = child.GetGeneration()
	receipt["egressLastSyncSuccess"] = false
	child.Object["status"] = receipt
	must(t, c.Status().Update(ctx, child))
	check("egress has not succeeded", group, desired, false)
	receipt["egressLastSyncSuccess"] = true
	child.Object["status"] = receipt
	must(t, c.Status().Update(ctx, child))
	check("current receipt for the closed rules", group, desired, true)

	opened := group.DeepCopy()
	opened.Generation++
	opened.Status.ObservedGeneration = opened.Generation
	opened.Status.Conditions[0].ObservedGeneration = opened.Generation
	opened.Spec.Ingress[0].CIDR = "10.60.0.0/24"
	check("Bedrock is ready before Fabric receives the open rules", opened, opened, false)
	openIngress, failure := network.FabricRules(opened.Namespace, network.NormalizeRules(opened.Spec.Ingress))
	if failure != nil {
		t.Fatalf("render open ingress: %v", failure)
	}
	child.Object["spec"] = fabric.SecurityGroup(fabric.Meta{}, openIngress, egress).Object["spec"]
	must(t, c.Update(ctx, child))
	check("Fabric still reports the closed rule generation", opened, opened, false)
	receipt["observedGeneration"] = child.GetGeneration()
	child.Object["status"] = receipt
	must(t, c.Status().Update(ctx, child))
	check("current receipt for the open rules", opened, opened, true)

	foreign := opened.DeepCopy()
	foreign.UID = uuid.NewUUID()
	child.SetAnnotations(network.OwnerAnnotations(foreign))
	must(t, c.Update(ctx, child))
	check("receipt belongs to another Bedrock group UID", opened, opened, false)
}
