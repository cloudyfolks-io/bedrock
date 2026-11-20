package operator

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func ovnCentralBundle(t *testing.T) release.Bundle {
	t.Helper()
	central := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": ovnCentralDeployment, "namespace": fabricNamespace},
		"spec": map[string]any{
			"replicas": int64(0),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "ovn-central"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "ovn-central"}},
				"spec": map[string]any{"containers": []any{map[string]any{
					"name": "ovn-central", "image": "example.com/ovn:1",
					"env": []any{map[string]any{"name": "NODE_IPS", "value": "${BEDROCK_MASTER_IPS}"}},
				}}},
			},
		},
	}}
	hook := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{
			"name": "fabric-post-upgrade-hook", "namespace": fabricNamespace,
			"annotations": map[string]any{"helm.sh/hook": "post-upgrade"},
		},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []any{map[string]any{
				"name": "hook", "image": "example.com/ovn:1",
				"env": []any{map[string]any{"name": "OVN_DB_IPS", "value": "${BEDROCK_MASTER_IPS}"}},
			}},
		}}},
	}}
	return release.Bundle{Spec: testBundle(t).Spec, Groups: []release.Group{{Order: 20, Name: "fabric", Objects: []*unstructured.Unstructured{central, hook}}}}
}

func addMasterNode(t *testing.T, c client.Client, name, ip string) {
	t.Helper()
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}}
	if err := c.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
}

func deleteMasterNode(t *testing.T, c client.Client, name string) {
	t.Helper()
	if err := c.Delete(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		t.Fatal(err)
	}
}

func liveCentralIPs(t *testing.T, c client.Client) (string, int64) {
	t.Helper()
	var deployment appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: fabricNamespace, Name: ovnCentralDeployment}, &deployment); err != nil {
		t.Fatal(err)
	}
	return deployment.Spec.Template.Spec.Containers[0].Env[0].Value, deployment.Generation
}

var (
	gateCalls atomic.Int32
	gateFails atomic.Bool
	gateSeen  centralSteps
)

type centralSteps struct {
	mu    sync.Mutex
	steps []string
}

func (s *centralSteps) add(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) == 0 || s.steps[len(s.steps)-1] != step {
		s.steps = append(s.steps, step)
	}
}

func (s *centralSteps) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = nil
}

func (s *centralSteps) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.steps)
}

func centralNodeIPs(obj *unstructured.Unstructured) string {
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	for _, container := range containers {
		env, _, _ := unstructured.NestedSlice(container.(map[string]any), "env")
		for _, entry := range env {
			if entry.(map[string]any)["name"] == "NODE_IPS" {
				return entry.(map[string]any)["value"].(string)
			}
		}
	}
	return ""
}

func fabricMastersReconciler(t *testing.T, c client.Client) *ClusterReconciler {
	t.Helper()
	if err := c.Create(context.Background(), newCluster("v0.1.0-test")); err != nil {
		t.Fatal(err)
	}
	gateSeen.reset()
	return &ClusterReconciler{Client: c, APIReader: c, Bundle: ovnCentralBundle(t), Gates: release.Gates{
		schema.GroupKind{Group: "apps", Kind: "Deployment"}: func(obj *unstructured.Unstructured) release.Readiness {
			gateCalls.Add(1)
			gateSeen.add(centralNodeIPs(obj))
			return release.Readiness{Ready: !gateFails.Load(), Failed: gateFails.Load()}
		},
		schema.GroupKind{Group: "batch", Kind: "Job"}: func(*unstructured.Unstructured) release.Readiness {
			return release.Readiness{Ready: true}
		},
	}, Interval: 50 * time.Millisecond, GroupTimeout: 5 * time.Second}
}

func TestFabricMastersFollowTheControlPlaneNodes(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	r := fabricMastersReconciler(t, c)
	addMasterNode(t, c, "host-1", "10.0.10.11")
	if err := r.install(ctx, 1, map[string]string{release.VarVIP: "10.0.0.10", release.VarMasterIPs: "10.0.10.11"}); err != nil {
		t.Fatal(err)
	}
	reconcile := func() {
		t.Helper()
		if err := r.reconcileFabricMasters(ctx); err != nil {
			t.Fatal(err)
		}
	}

	reconcile()
	ips, generation := liveCentralIPs(t, c)
	if ips != "10.0.10.11" {
		t.Fatalf("unchanged set: NODE_IPS %q", ips)
	}

	addMasterNode(t, c, "host-2", "10.0.10.12")
	addMasterNode(t, c, "host-3", "10.0.10.13")
	reconcile()
	grown, grownGeneration := liveCentralIPs(t, c)
	if grown != "10.0.10.11,10.0.10.12,10.0.10.13" {
		t.Fatalf("after the joins: NODE_IPS %q", grown)
	}

	reconcile()
	if _, again := liveCentralIPs(t, c); again != grownGeneration || grownGeneration == generation {
		t.Fatalf("an unchanged set must not apply again: generation %d, %d, %d", generation, grownGeneration, again)
	}

	deleteMasterNode(t, c, "host-3")
	reconcile()
	if shrunk, _ := liveCentralIPs(t, c); shrunk != "10.0.10.11,10.0.10.12" {
		t.Fatalf("after the removal: NODE_IPS %q", shrunk)
	}
}

func TestFabricMastersWaitForTheInstall(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	r := fabricMastersReconciler(t, c)
	addMasterNode(t, c, "host-1", "10.0.10.11")
	if err := r.reconcileFabricMasters(ctx); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: fabricNamespace, Name: ovnCentralDeployment}, &deployment); err == nil {
		t.Fatal("nothing must be applied before the install")
	}
}

func TestFabricMastersRetryTheWaitUntilItSucceeds(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	gateFails.Store(false)
	r := fabricMastersReconciler(t, c)
	addMasterNode(t, c, "host-1", "10.0.10.11")
	if err := r.install(ctx, 1, map[string]string{release.VarVIP: "10.0.0.10", release.VarMasterIPs: "10.0.10.11"}); err != nil {
		t.Fatal(err)
	}
	addMasterNode(t, c, "host-2", "10.0.10.12")
	gateFails.Store(true)
	if err := r.reconcileFabricMasters(ctx); err == nil {
		t.Fatal("a failed wait must fail the reconcile")
	}
	if ips, _ := liveCentralIPs(t, c); ips != "10.0.10.11,10.0.10.12" {
		t.Fatalf("the apply must have happened: NODE_IPS %q", ips)
	}
	if condition := clusterConditionOf(t, c, conditionFabricMastersApplied); condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", condition)
	}

	gateFails.Store(false)
	before := gateCalls.Load()
	if err := r.reconcileFabricMasters(ctx); err != nil {
		t.Fatal(err)
	}
	if gateCalls.Load() == before {
		t.Fatal("the next reconcile must retry the wait")
	}
	if condition := clusterConditionOf(t, c, conditionFabricMastersApplied); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", condition)
	}

	settled := gateCalls.Load()
	if err := r.reconcileFabricMasters(ctx); err != nil {
		t.Fatal(err)
	}
	if gateCalls.Load() != settled {
		t.Fatal("a completed set must not run the apply again")
	}
}

func TestFabricMastersRecordWithoutApplyWhenTheLiveSetMatches(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	gateFails.Store(false)
	r := fabricMastersReconciler(t, c)
	addMasterNode(t, c, "host-1", "10.0.10.11")
	if err := r.install(ctx, 1, map[string]string{release.VarVIP: "10.0.0.10", release.VarMasterIPs: "10.0.10.11"}); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: fabricNamespace, Name: ovnCentralDeployment}, &deployment); err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	deployment.Spec.Replicas = &replicas
	if err := c.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1}
	if err := c.Status().Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	before := gateCalls.Load()
	if err := r.reconcileFabricMasters(ctx); err != nil {
		t.Fatal(err)
	}
	if gateCalls.Load() != before {
		t.Fatal("a matching ready Deployment must not be applied")
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Annotations[fabricMastersAppliedAnnotation] != "10.0.10.11" {
		t.Fatalf("annotations %v", cluster.Annotations)
	}
	if condition := clusterConditionOf(t, c, conditionFabricMastersApplied); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", condition)
	}
}

func installedOneMasterThenJoined(t *testing.T, c client.Client, r *ClusterReconciler, joined ...string) {
	t.Helper()
	addMasterNode(t, c, "host-1", "10.0.10.11")
	if err := r.install(context.Background(), 1, map[string]string{release.VarVIP: "10.0.0.10", release.VarMasterIPs: "10.0.10.11"}); err != nil {
		t.Fatal(err)
	}
	for i, ip := range joined {
		addMasterNode(t, c, "host-"+string(rune('2'+i)), ip)
	}
}

func TestFabricMastersRecreateTheCompletedHookJob(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	gateFails.Store(false)
	r := fabricMastersReconciler(t, c)
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	var before batchv1.Job
	key := client.ObjectKey{Namespace: fabricNamespace, Name: "fabric-post-upgrade-hook"}
	if err := c.Get(ctx, key, &before); !apierrors.IsNotFound(err) {
		t.Fatalf("initial install must omit the upgrade Job: %v", err)
	}
	vars := map[string]string{release.VarVIP: "10.0.0.10", release.VarMasterIPs: "10.0.10.11"}
	if err := release.UpgradeGroup(ctx, c, r.Bundle, vars, r.Gates, r.Interval, r.GroupTimeout, fabricGroupName, release.SkipHook, release.SkipHook); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &before); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileFabricMasters(ctx); err != nil {
		t.Fatalf("a completed hook Job must not block the re-render: %v", err)
	}
	var after batchv1.Job
	if err := c.Get(ctx, key, &after); err != nil {
		t.Fatal(err)
	}
	if after.UID == before.UID {
		t.Fatal("the hook Job must be recreated")
	}
	if got := after.Spec.Template.Spec.Containers[0].Env[0].Value; got != "10.0.10.11,10.0.10.12,10.0.10.13" {
		t.Fatalf("OVN_DB_IPS %q", got)
	}
}

func TestFabricMastersGrowOneMemberAtATime(t *testing.T) {
	c, _ := StartTestEnv(t)
	gateFails.Store(false)
	r := fabricMastersReconciler(t, c)
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	gateSeen.reset()
	if err := r.reconcileFabricMasters(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.10.11,10.0.10.12", "10.0.10.11,10.0.10.12,10.0.10.13"}
	if got := gateSeen.list(); !slices.Equal(got, want) {
		t.Fatalf("members waited for in order: %v, want %v", got, want)
	}
}

func TestFabricMastersStopGrowingWhenAMemberDoesNotJoin(t *testing.T) {
	c, _ := StartTestEnv(t)
	gateFails.Store(false)
	r := fabricMastersReconciler(t, c)
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	gateFails.Store(true)
	if err := r.reconcileFabricMasters(context.Background()); err == nil {
		t.Fatal("a member that does not become ready must fail the reconcile")
	}
	if ips, _ := liveCentralIPs(t, c); ips != "10.0.10.11,10.0.10.12" {
		t.Fatalf("the next member must wait for the previous one: NODE_IPS %q", ips)
	}
	var hook batchv1.Job
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: fabricNamespace, Name: "fabric-post-upgrade-hook"}, &hook); err != nil {
		t.Fatal(err)
	}
	if got := hook.Spec.Template.Spec.Containers[0].Env[0].Value; got != "10.0.10.11" {
		t.Fatalf("consumers must keep the old members until the database grew: OVN_DB_IPS %q", got)
	}
	if condition := clusterConditionOf(t, c, conditionFabricMastersApplied); condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", condition)
	}
}
