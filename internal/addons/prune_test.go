package addons

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func createPod(t *testing.T, c client.Client, namespace, name string, podLabels map[string]string) *corev1.Pod {
	t.Helper()
	merged := map[string]string{"test": "addons"}
	for key, value := range podLabels {
		merged[key] = value
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: merged},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "example.test/c:1"}}},
	}
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func disable(t *testing.T, c client.Client, name string) {
	t.Helper()
	updateAddon(t, c, name, func(a *v1alpha1.Addon) { a.Spec.Enabled = false })
}

func TestPruneWaitsForWorkloadPods(t *testing.T) {
	r, c := enabledKataFixture(t)
	pod := createPod(t, c, "kube-system", "kata-deploy-abc", map[string]string{"name": "kata-deploy"})
	disable(t, c, "kata")
	result := runReconcile(t, r, "kata")
	if exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") {
		t.Fatal("the DaemonSet stayed")
	}
	if !exists(c, &corev1.ServiceAccount{}, "kube-system", "kata-deploy-sa") || !exists(c, &rbacv1.ClusterRole{}, "", "kata-deploy-role") || !exists(c, &rbacv1.ClusterRoleBinding{}, "", "kata-deploy-rb") {
		t.Fatal("the RBAC went while a pod runs")
	}
	if got := condition(t, c, "kata", "Ready"); got.Reason != "Pruning" || got.Status != metav1.ConditionFalse {
		t.Fatalf("ready %+v", got)
	}
	if result.RequeueAfter != pruneInterval {
		t.Errorf("requeue %v", result.RequeueAfter)
	}
	if err := c.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, "kata")
	if exists(c, &rbacv1.ClusterRole{}, "", "kata-deploy-role") || exists(c, &corev1.ServiceAccount{}, "kube-system", "kata-deploy-sa") || exists(c, &nodev1.RuntimeClass{}, "", "kata-qemu") {
		t.Fatal("objects stayed after the pod ended")
	}
	if exists(c, &corev1.ConfigMap{}, "bedrock-system", "bedrock-addon-kata-inventory") {
		t.Fatal("the inventory stayed")
	}
	if got := condition(t, c, "kata", "Ready"); got.Reason != "Disabled" {
		t.Fatalf("ready %+v", got)
	}
}

func TestPruneKeepsWhatIsNotInTheInventory(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "user-config"}}); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "storage-loki-0"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	if err := c.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	disable(t, c, "loki")
	runReconcile(t, r, "loki")
	if exists(c, &appsv1.StatefulSet{}, "monitoring", "loki") || exists(c, &corev1.ConfigMap{}, "monitoring", "loki") {
		t.Fatal("loki objects stayed")
	}
	if !exists(c, &corev1.ConfigMap{}, "monitoring", "user-config") || !exists(c, &corev1.PersistentVolumeClaim{}, "monitoring", "storage-loki-0") {
		t.Fatal("an object outside the inventory was removed")
	}
}

func TestPruneNeverDeletesNamespaces(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	inventory := inventoryOf(testBundle.Addons["loki"])
	inventory[release.RefOf(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "monitoring"}}})] = struct{}{}
	if err := writeInventory(context.Background(), c, "loki", inventory); err != nil {
		t.Fatal(err)
	}
	disable(t, c, "loki")
	runReconcile(t, r, "loki")
	var namespace corev1.Namespace
	if err := c.Get(context.Background(), client.ObjectKey{Name: "monitoring"}, &namespace); err != nil || !namespace.DeletionTimestamp.IsZero() {
		t.Fatalf("namespace: %v %v", err, namespace.DeletionTimestamp)
	}
}

func TestDisableTwiceIsQuiet(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", false, "")
	runReconcile(t, r, "loki")
	first := condition(t, c, "loki", "Ready")
	runReconcile(t, r, "loki")
	second := condition(t, c, "loki", "Ready")
	if first.Reason != "Disabled" || second.Reason != "Disabled" || !first.LastTransitionTime.Equal(&second.LastTransitionTime) {
		t.Fatalf("first %+v second %+v", first, second)
	}
}

func TestFinalizerPrunesBeforeDelete(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	pod := createPod(t, c, "monitoring", "loki-0", map[string]string{"app.kubernetes.io/name": "loki"})
	if err := c.Delete(context.Background(), &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "loki"}}); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, "loki")
	held := getAddon(t, c, "loki")
	if held.DeletionTimestamp.IsZero() {
		t.Fatal("the delete was not recorded")
	}
	if !exists(c, &corev1.ServiceAccount{}, "monitoring", "loki") {
		t.Fatal("the objects went before the pod ended")
	}
	if err := c.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, "loki")
	var gone v1alpha1.Addon
	if err := c.Get(context.Background(), client.ObjectKey{Name: "loki"}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("addon stayed: %v", err)
	}
	if exists(c, &corev1.ServiceAccount{}, "monitoring", "loki") {
		t.Fatal("the objects stayed")
	}
}

func TestReEnableAfterPruneAppliesAgain(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	disable(t, c, "loki")
	runReconcile(t, r, "loki")
	if exists(c, &appsv1.StatefulSet{}, "monitoring", "loki") {
		t.Fatal("not pruned")
	}
	updateAddon(t, c, "loki", func(a *v1alpha1.Addon) { a.Spec.Enabled = true })
	runReconcile(t, r, "loki")
	if !exists(c, &appsv1.StatefulSet{}, "monitoring", "loki") {
		t.Fatal("not applied again")
	}
}

func TestPartialApplyIsStillPrunedWhenDisabled(t *testing.T) {
	r, c := newFixture(t)
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "w", "namespace": "kube-system"}}}
	bundle := testBundle
	bundle.Addons = map[string][]*unstructured.Unstructured{"kata": append(append([]*unstructured.Unstructured{}, testBundle.Addons["kata"]...), widget)}
	r.Bundle = bundle
	addHost(t, c, "w1", v1alpha1.RoleWorkload, boolPtr(true), true)
	createAddon(t, c, "kata", true, "")
	runReconcile(t, r, "kata")
	if got := condition(t, c, "kata", "Ready"); got.Reason != "MissingCRD" {
		t.Fatalf("ready %+v", got)
	}
	if !exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") {
		t.Fatal("the workload was not applied before the failure")
	}
	disable(t, c, "kata")
	runReconcile(t, r, "kata")
	if exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") || exists(c, &rbacv1.ClusterRole{}, "", "kata-deploy-role") || exists(c, &corev1.ServiceAccount{}, "kube-system", "kata-deploy-sa") {
		t.Fatal("an object of the partial apply stayed")
	}
}
