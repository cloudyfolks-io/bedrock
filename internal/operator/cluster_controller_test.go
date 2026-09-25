package operator

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func testBundle(t *testing.T) release.Bundle {
	t.Helper()
	bundle, err := release.Load(os.DirFS(filepath.Join("..", "release", "testdata", "good")))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func testBundleUsingMasterIPs(t *testing.T) release.Bundle {
	t.Helper()
	bundle := testBundle(t)
	for _, group := range bundle.Groups {
		for _, obj := range group.Objects {
			if obj.GetKind() == "ConfigMap" && obj.GetName() == "beta" {
				if err := unstructured.SetNestedField(obj.Object, "${BEDROCK_MASTER_IPS}", "data", "key"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return bundle
}

func newCluster(version string) *v1alpha1.Cluster {
	return &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: version, API: v1alpha1.APISpec{VIP: "10.0.0.10", VIPMode: "arp"}, NodeConcurrency: 1}}
}

func createMasterNode(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "master-1", Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.10.11"}}
	if err := c.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
}

func TestClusterReconcilerInstallsEmbeddedRelease(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	r := &ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: testBundle(t), Gates: release.Gates{}, Interval: 200 * time.Millisecond, GroupTimeout: 20 * time.Second}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	createMasterNode(t, ctx, c)
	if err := c.Create(ctx, newCluster("v0.1.0-test")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
			return false
		}
		return got.Status.Version == "v0.1.0-test" && got.Status.Phase == v1alpha1.PhaseIdle && v1alpha1.IsConditionTrue(got.Status.Conditions, v1alpha1.ConditionAvailable)
	})
	var got v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Components) != 2 || !got.Status.Components[0].Available || !got.Status.Components[1].Available {
		t.Fatalf("components %+v", got.Status.Components)
	}
	var rel v1alpha1.Release
	if err := c.Get(ctx, client.ObjectKey{Name: "v0.1.0-test"}, &rel); err != nil {
		t.Fatalf("release object: %v", err)
	}
	if !v1alpha1.IsConditionTrue(rel.Status.Conditions, v1alpha1.ConditionReady) {
		t.Fatalf("release status %+v", rel.Status)
	}
	var alpha corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); err != nil {
		t.Fatalf("release objects not applied: %v", err)
	}
	inv, err := release.ReadInventory(ctx, c)
	if err != nil || len(inv) != 4 {
		t.Fatalf("inventory %v %v", inv, err)
	}
}

func TestClusterReconcilerAwaitsUpgradeForOtherVersion(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	r := &ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: testBundle(t), Gates: release.Gates{}, Interval: 200 * time.Millisecond, GroupTimeout: 20 * time.Second}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	createMasterNode(t, ctx, c)
	if err := c.Create(ctx, newCluster("v0.2.0")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
			return false
		}
		for _, cond := range got.Status.Conditions {
			if cond.Type == v1alpha1.ConditionProgressing && cond.Status == metav1.ConditionFalse && cond.Reason == "AwaitingUpgrade" {
				return true
			}
		}
		return false
	})
	var alpha corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); err == nil {
		t.Fatal("nothing must be applied for a non-embedded version")
	}
}

func TestClusterReconcilerFailsAndResumes(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	var blocked atomic.Bool
	blocked.Store(true)
	gate := func(*unstructured.Unstructured) release.Readiness {
		if blocked.Load() {
			return release.Readiness{Message: "blocked by test"}
		}
		return release.Readiness{Ready: true}
	}
	r := &ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: testBundle(t), Gates: release.Gates{schema.GroupKind{Kind: "ConfigMap"}: gate}, Interval: 100 * time.Millisecond, GroupTimeout: 1 * time.Second}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	createMasterNode(t, ctx, c)
	if err := c.Create(ctx, newCluster("v0.1.0-test")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
			return false
		}
		return got.Status.Phase == v1alpha1.PhaseFailed && v1alpha1.IsConditionTrue(got.Status.Conditions, v1alpha1.ConditionDegraded)
	})
	var failed v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Status.Components[1].Available || !failed.Status.Components[1].Degraded {
		t.Fatalf("components %+v", failed.Status.Components)
	}

	blocked.Store(false)
	failed.Spec.Upgrade.Action = v1alpha1.UpgradeActionResume
	if err := c.Update(ctx, &failed); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
			return false
		}
		return got.Status.Phase == v1alpha1.PhaseIdle && got.Status.Version == "v0.1.0-test" && got.Spec.Upgrade.Action == ""
	})
}

func TestClusterReconcilerWaitsForMasterNodes(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	r := &ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: testBundleUsingMasterIPs(t), Gates: release.Gates{}, Interval: 200 * time.Millisecond, GroupTimeout: 20 * time.Second}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	if err := c.Create(ctx, newCluster("v0.1.0-test")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
			return false
		}
		for _, cond := range got.Status.Conditions {
			if cond.Type == v1alpha1.ConditionProgressing && cond.Status == metav1.ConditionTrue && cond.Reason == "WaitingForMasterNodes" {
				return true
			}
		}
		return false
	})
	var alpha corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); err == nil {
		t.Fatal("nothing must be applied while waiting for master nodes")
	}
}

func TestClusterReconcilerInstallsWithoutMasterNodesWhenBundleDoesNotNeedThem(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	r := &ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: testBundle(t), Gates: release.Gates{}, Interval: 200 * time.Millisecond, GroupTimeout: 20 * time.Second}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	if err := c.Create(ctx, newCluster("v0.1.0-test")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var got v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
			return false
		}
		return got.Status.Phase == v1alpha1.PhaseIdle && v1alpha1.IsConditionTrue(got.Status.Conditions, v1alpha1.ConditionAvailable)
	})
	var got v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &got); err != nil {
		t.Fatal(err)
	}
	for _, cond := range got.Status.Conditions {
		if cond.Reason == "WaitingForMasterNodes" {
			t.Fatal("bundle without the master placeholder must not wait for master nodes")
		}
	}
}
