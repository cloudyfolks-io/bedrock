package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

func TestHostReconcileKeepsTheGuardWhenTheClusterIsMissing(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v0.1.0", API: v1alpha1.APISpec{VIP: "10.0.0.10", VIPMode: "arp"}, NodeConcurrency: 1, TenantLegs: []v1alpha1.TenantLegSpec{{Interface: "br-public.30"}}}}
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "keep-node", Labels: roles.Labels([]string{v1alpha1.RoleWorkload})}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "keep-node"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleWorkload}, Management: v1alpha1.ManagementSpec{Enabled: true}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	r := &HostReconciler{Client: c, APIReader: c}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: "keep-node"}}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err == nil {
		t.Fatal("a missing Cluster must be retried")
	}
	var hc v1alpha1.HostConfig
	if err := c.Get(ctx, client.ObjectKey{Name: "keep-node"}, &hc); err != nil {
		t.Fatal(err)
	}
	if len(hc.Spec.TenantLegs) != 1 || hc.Spec.TenantLegs[0].Interface != "br-public.30" {
		t.Fatalf("the guard must stay: %+v", hc.Spec.TenantLegs)
	}
}
