package operator

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

func TestHostReconcilerLabelsNode(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&HostReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"kubernetes.io/hostname": "node-1"}}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane, v1alpha1.RoleCephOSD}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		var got corev1.Node
		if err := c.Get(ctx, client.ObjectKey{Name: "node-1"}, &got); err != nil {
			return false
		}
		return got.Labels["bedrock.cloudyfolks.io/role-ceph-osd"] == "true" && len(got.Spec.Taints) == 1
	})
	waitFor(t, func() bool {
		var got v1alpha1.Host
		if err := c.Get(ctx, client.ObjectKey{Name: "node-1"}, &got); err != nil {
			return false
		}
		return v1alpha1.IsConditionTrue(got.Status.Conditions, v1alpha1.ConditionReady) && got.Status.ObservedGeneration == got.Generation
	})

	var current v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "node-1"}, &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Roles = []string{v1alpha1.RoleWorkload}
	if err := c.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got corev1.Node
		if err := c.Get(ctx, client.ObjectKey{Name: "node-1"}, &got); err != nil {
			return false
		}
		_, stale := got.Labels["bedrock.cloudyfolks.io/role-ceph-osd"]
		return !stale && len(got.Spec.Taints) == 0 && got.Labels["kubernetes.io/hostname"] == "node-1"
	})
}

func TestHostReconcilerWithoutNode(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&HostReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "ghost"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleWorkload}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Host
		if err := c.Get(ctx, client.ObjectKey{Name: "ghost"}, &got); err != nil {
			return false
		}
		return len(got.Status.Conditions) == 1 && got.Status.Conditions[0].Reason == "NodeMissing"
	})
}

func TestHostReconcilerCreatesHostFromNodeLabels(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&HostReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "joined-1", Labels: map[string]string{"bedrock.cloudyfolks.io/role-workload": "true", "bedrock.cloudyfolks.io/role-ceph-osd": "true"}}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Host
		if err := c.Get(ctx, client.ObjectKey{Name: "joined-1"}, &got); err != nil {
			return false
		}
		return len(got.Spec.Roles) == 2 && got.Spec.Roles[0] == v1alpha1.RoleCephOSD && got.Spec.Roles[1] == v1alpha1.RoleWorkload && got.Labels[v1alpha1.LabelKind] == "Host"
	})

	plain := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "plain-1"}}
	if err := c.Create(ctx, plain); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	var ghost v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "plain-1"}, &ghost); err == nil {
		t.Fatal("node without role labels must not get a Host")
	}
}

func TestHostReconcileRendersHostConfigAndManagedLabel(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "hc-node", Labels: roles.Labels([]string{v1alpha1.RoleWorkload})}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "hc-node"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleWorkload}, Management: v1alpha1.ManagementSpec{Enabled: true}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	r := &HostReconciler{Client: c}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "hc-node"}}); err != nil {
		t.Fatal(err)
	}
	var hc v1alpha1.HostConfig
	if err := c.Get(ctx, client.ObjectKey{Name: "hc-node"}, &hc); err != nil {
		t.Fatal(err)
	}
	if len(hc.Spec.Modules) != 4 || len(hc.OwnerReferences) != 1 || hc.OwnerReferences[0].Name != "hc-node" {
		t.Fatalf("hostconfig %+v", hc)
	}
	if hc.Labels[v1alpha1.LabelKind] != "HostConfig" || hc.Labels[v1alpha1.LabelName] != "hc-node" {
		t.Fatalf("hostconfig labels %v", hc.Labels)
	}
	var got corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: "hc-node"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[v1alpha1.LabelManaged] != "true" {
		t.Fatalf("labels %v", got.Labels)
	}

	var current v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "hc-node"}, &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Management.Enabled = false
	if err := c.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "hc-node"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "hc-node"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[v1alpha1.LabelManaged] != "false" {
		t.Fatalf("labels after disable %v", got.Labels)
	}
}

func TestHostReconcileRendersMirrorFromCluster(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v0.1.0", API: v1alpha1.APISpec{VIP: "10.0.0.10", VIPMode: "arp"}, NodeConcurrency: 1, Registry: v1alpha1.RegistrySpec{Mirror: "https://m.example"}}}
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "mirror-node", Labels: roles.Labels([]string{v1alpha1.RoleWorkload})}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "mirror-node"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleWorkload}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	r := &HostReconciler{Client: c}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "mirror-node"}}); err != nil {
		t.Fatal(err)
	}
	var hc v1alpha1.HostConfig
	if err := c.Get(ctx, client.ObjectKey{Name: "mirror-node"}, &hc); err != nil {
		t.Fatal(err)
	}
	if len(hc.Spec.ContainerdMirrors) != 1 || hc.Spec.ContainerdMirrors[0].Endpoint != "https://m.example" {
		t.Fatalf("mirrors %+v", hc.Spec.ContainerdMirrors)
	}
	var got corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: "mirror-node"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[v1alpha1.LabelManaged] != "false" {
		t.Fatalf("labels %v", got.Labels)
	}
}
