package operator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
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

func fabricAgentBundle(t *testing.T) release.Bundle {
	t.Helper()
	agent := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]any{"name": "fabric-cni", "namespace": "kube-system"},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]any{"app": "fabric-cni"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "fabric-cni"}},
				"spec": map[string]any{"containers": []any{map[string]any{
					"name": "cni-server", "image": "example.com/cni:1",
					"args": []any{"--service-cluster-ip-range=10.96.0.0/12", "--iface=eth0"},
				}}},
			},
		},
	}}
	return release.Bundle{Spec: testBundle(t).Spec, Groups: []release.Group{{Order: 20, Name: "fabric", Objects: []*unstructured.Unstructured{agent}}}}
}

func TestClusterReconcilerInstallsTheFabricAgentWithThePodMTU(t *testing.T) {
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
	r := &ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: fabricAgentBundle(t), Gates: release.Gates{}, Interval: 200 * time.Millisecond, GroupTimeout: 20 * time.Second}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	createMasterNode(t, ctx, c)
	cluster := newCluster("v0.1.0-test")
	cluster.Spec.PodMTU = 1500
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var agent appsv1.DaemonSet
		if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "fabric-cni"}, &agent); err != nil {
			return false
		}
		args := agent.Spec.Template.Spec.Containers[0].Args
		return slices.Equal(args, []string{"--service-cluster-ip-range=10.96.0.0/12", "--mtu=1500", "--iface=eth0"})
	})
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

func TestWriteClusterStatusKeepsAnAdoptedReason(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	adopted := metav1.Condition{Type: v1alpha1.ConditionStorageAdopted, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonAdopted, LastTransitionTime: metav1.Now()}
	createClusterWithStatus(t, ctx, c, "v1", v1alpha1.ClusterStatus{Version: "v1", Conditions: []metav1.Condition{adopted}})
	stale := metav1.Condition{Type: v1alpha1.ConditionStorageAdopted, Status: metav1.ConditionFalse, Reason: v1alpha1.ReasonWaitingForQuorum, Message: "stale"}
	ready := metav1.Condition{Type: v1alpha1.ConditionStorageReady, Status: metav1.ConditionFalse, Reason: "Progressing"}
	err := writeClusterStatus(ctx, c, func(s *v1alpha1.ClusterStatus) {
		v1alpha1.SetCondition(&s.Conditions, stale)
		v1alpha1.SetCondition(&s.Conditions, ready)
	})
	if err != nil {
		t.Fatal(err)
	}
	conditions := getCluster(t, ctx, c).Status.Conditions
	got := meta.FindStatusCondition(conditions, v1alpha1.ConditionStorageAdopted)
	if got == nil || got.Status != metav1.ConditionTrue || got.Reason != v1alpha1.ReasonAdopted || got.Message != "" {
		t.Fatalf("StorageAdopted %+v", got)
	}
	if other := meta.FindStatusCondition(conditions, v1alpha1.ConditionStorageReady); other == nil || other.Reason != "Progressing" {
		t.Fatalf("StorageReady %+v", other)
	}
}

func TestWriteClusterStatusMovesAnUnadoptedReason(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	waiting := metav1.Condition{Type: v1alpha1.ConditionStorageAdopted, Status: metav1.ConditionFalse, Reason: v1alpha1.ReasonWaitingForQuorum, LastTransitionTime: metav1.Now()}
	createClusterWithStatus(t, ctx, c, "v1", v1alpha1.ClusterStatus{Version: "v1", Conditions: []metav1.Condition{waiting}})
	next := metav1.Condition{Type: v1alpha1.ConditionStorageAdopted, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonAdopted}
	if err := writeClusterStatus(ctx, c, func(s *v1alpha1.ClusterStatus) { v1alpha1.SetCondition(&s.Conditions, next) }); err != nil {
		t.Fatal(err)
	}
	got := meta.FindStatusCondition(getCluster(t, ctx, c).Status.Conditions, v1alpha1.ConditionStorageAdopted)
	if got == nil || got.Reason != v1alpha1.ReasonAdopted {
		t.Fatalf("StorageAdopted %+v", got)
	}
}
