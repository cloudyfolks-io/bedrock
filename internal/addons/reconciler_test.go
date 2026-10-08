package addons

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var addonLabels = client.MatchingLabels{v1alpha1.LabelKind: "Addon"}

func resetEnv(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	resetAddonsAndSettings(t, c)
	_ = c.DeleteAllOf(ctx, &v1alpha1.Host{})
	_ = c.DeleteAllOf(ctx, &v1alpha1.Cluster{})
	_ = c.DeleteAllOf(ctx, &corev1.Node{})
	for _, namespace := range []string{"monitoring", "bedrock-system", "kube-system"} {
		in := client.InNamespace(namespace)
		_ = c.DeleteAllOf(ctx, &corev1.Pod{}, in, client.MatchingLabels{"test": "addons"})
		_ = c.DeleteAllOf(ctx, &appsv1.StatefulSet{}, in, addonLabels)
		_ = c.DeleteAllOf(ctx, &appsv1.DaemonSet{}, in, addonLabels)
		_ = c.DeleteAllOf(ctx, &corev1.ConfigMap{}, in, client.MatchingLabels{v1alpha1.LabelKind: "Addon"})
		_ = c.DeleteAllOf(ctx, &corev1.ServiceAccount{}, in, addonLabels)
		_ = c.DeleteAllOf(ctx, &corev1.Service{}, in, addonLabels)
	}
	for _, name := range []string{"user-config", "loki-old"} {
		_ = c.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: name}})
	}
	_ = c.DeleteAllOf(ctx, &corev1.PersistentVolumeClaim{}, client.InNamespace("monitoring"))
	_ = c.DeleteAllOf(ctx, &rbacv1.ClusterRole{}, addonLabels)
	_ = c.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, addonLabels)
	_ = c.DeleteAllOf(ctx, &nodev1.RuntimeClass{}, addonLabels)
}

func newFixture(t *testing.T) (*Reconciler, client.Client) {
	t.Helper()
	c := newClient(t)
	resetEnv(t, c)
	t.Cleanup(func() { resetEnv(t, c) })
	setClusterVersion(t, c, "v1")
	return &Reconciler{Client: c, Bundle: testBundle, Interval: time.Second, ReadyInterval: time.Minute}, c
}

func setClusterVersion(t *testing.T, c client.Client, version string) {
	t.Helper()
	ctx := context.Background()
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v1", API: v1alpha1.APISpec{VIP: "10.0.0.250"}}}
	if err := c.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Status.Version = version
	if err := c.Status().Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
}

func createAddon(t *testing.T, c client.Client, name string, enabled bool, values string) {
	t.Helper()
	addon := &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AddonSpec{Enabled: enabled}}
	if values != "" {
		addon.Spec.Values = jsonOf([]byte(values))
	}
	if err := c.Create(context.Background(), addon); err != nil {
		t.Fatal(err)
	}
}

func updateAddon(t *testing.T, c client.Client, name string, change func(*v1alpha1.Addon)) {
	t.Helper()
	addon := getAddon(t, c, name)
	change(&addon)
	if err := c.Update(context.Background(), &addon); err != nil {
		t.Fatal(err)
	}
}

func runReconcile(t *testing.T, r *Reconciler, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func condition(t *testing.T, c client.Client, addon, conditionType string) metav1.Condition {
	t.Helper()
	found := meta.FindStatusCondition(getAddon(t, c, addon).Status.Conditions, conditionType)
	if found == nil {
		t.Fatalf("addon %s has no %s condition", addon, conditionType)
	}
	return *found
}

func addHost(t *testing.T, c client.Client, name string, role string, kvm *bool, reported bool) {
	t.Helper()
	ctx := context.Background()
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{role}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	if reported {
		host.Status.Checks = &v1alpha1.HostChecks{KVM: kvm}
		if err := c.Status().Update(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
}

func setHostKVM(t *testing.T, c client.Client, name string, kvm bool) {
	t.Helper()
	ctx := context.Background()
	var host v1alpha1.Host
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Checks = &v1alpha1.HostChecks{KVM: &kvm}
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
}

func exists(c client.Client, obj client.Object, namespace, name string) bool {
	err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
	return err == nil
}

func markWorkloadsReady(t *testing.T, c client.Client, addon string) {
	t.Helper()
	ctx := context.Background()
	for _, obj := range testBundle.Addons[addon] {
		key := client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()}
		switch obj.GetKind() {
		case "StatefulSet":
			var live appsv1.StatefulSet
			if err := c.Get(ctx, key, &live); err != nil {
				t.Fatal(err)
			}
			live.Status = appsv1.StatefulSetStatus{ObservedGeneration: live.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
			if err := c.Status().Update(ctx, &live); err != nil {
				t.Fatal(err)
			}
		case "DaemonSet":
			var live appsv1.DaemonSet
			if err := c.Get(ctx, key, &live); err != nil {
				t.Fatal(err)
			}
			live.Status = appsv1.DaemonSetStatus{ObservedGeneration: live.Generation, DesiredNumberScheduled: 1, NumberReady: 1, UpdatedNumberScheduled: 1}
			if err := c.Status().Update(ctx, &live); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func enabledKataFixture(t *testing.T) (*Reconciler, client.Client) {
	t.Helper()
	r, c := newFixture(t)
	addHost(t, c, "w1", v1alpha1.RoleWorkload, boolPtr(true), true)
	createAddon(t, c, "kata", true, "")
	runReconcile(t, r, "kata")
	return r, c
}

func claimOf(t *testing.T, c client.Client) (string, string) {
	t.Helper()
	var statefulSet appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "monitoring", Name: "loki"}, &statefulSet); err != nil {
		t.Fatal(err)
	}
	claim := statefulSet.Spec.VolumeClaimTemplates[0]
	size := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	return size.String(), *claim.Spec.StorageClassName
}

func TestReconcileAppliesWhenEnabled(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	for _, obj := range testBundle.Addons["loki"] {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GroupVersionKind())
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), live); err != nil {
			t.Fatalf("%s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
		if live.GetLabels()["bedrock.cloudyfolks.io/kind"] != "Addon" || live.GetLabels()["bedrock.cloudyfolks.io/name"] != "loki" {
			t.Errorf("%s %s labels %v", obj.GetKind(), obj.GetName(), live.GetLabels())
		}
	}
	if size, class := claimOf(t, c); size != "20Gi" || class != "block" {
		t.Errorf("claim %s %s", size, class)
	}
	if !exists(c, &corev1.ConfigMap{}, "bedrock-system", "bedrock-addon-loki-inventory") {
		t.Error("inventory missing")
	}
}

func TestApplyUsesTheAddonsFieldManager(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	var statefulSet appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "monitoring", Name: "loki"}, &statefulSet); err != nil {
		t.Fatal(err)
	}
	for _, entry := range statefulSet.ManagedFields {
		if entry.Manager == v1alpha1.AddonsFieldManager {
			return
		}
	}
	t.Fatalf("managed fields %+v", statefulSet.ManagedFields)
}

func TestReconcileReportsProgressingUntilWorkloadsAreReady(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	if got := condition(t, c, "loki", "Ready"); got.Status != metav1.ConditionFalse || got.Reason != "Progressing" {
		t.Fatalf("before: %+v", got)
	}
	markWorkloadsReady(t, c, "loki")
	result := runReconcile(t, r, "loki")
	if got := condition(t, c, "loki", "Ready"); got.Status != metav1.ConditionTrue || got.Reason != "Ready" {
		t.Fatalf("after: %+v", got)
	}
	if result.RequeueAfter != r.ReadyInterval {
		t.Errorf("requeue %v", result.RequeueAfter)
	}
	if getAddon(t, c, "loki").Status.ObservedGeneration != getAddon(t, c, "loki").Generation {
		t.Error("observedGeneration is behind")
	}
}

func TestValuesReachTheObjects(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, `{"persistence.size":"1Gi","persistence.storage-class":"e2e-local"}`)
	runReconcile(t, r, "loki")
	if size, class := claimOf(t, c); size != "1Gi" || class != "e2e-local" {
		t.Fatalf("claim %s %s", size, class)
	}
}

func TestInvalidValuesHoldTheCurrentState(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	updateAddon(t, c, "loki", func(a *v1alpha1.Addon) { a.Spec.Values = jsonOf([]byte(`{"persistence.nothing":"x"}`)) })
	runReconcile(t, r, "loki")
	if got := condition(t, c, "loki", "Ready"); got.Reason != "InvalidValues" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	if !exists(c, &appsv1.StatefulSet{}, "monitoring", "loki") || !exists(c, &appsv1.DaemonSet{}, "monitoring", "alloy") {
		t.Fatal("an applied addon was pruned")
	}
}

func hashOf(t *testing.T, c client.Client) string {
	t.Helper()
	var statefulSet appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "monitoring", Name: "loki"}, &statefulSet); err != nil {
		t.Fatal(err)
	}
	return statefulSet.Spec.Template.Annotations[AnnotationConfigHash]
}

func TestRetentionChangeRollsTheStatefulSet(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	before := hashOf(t, c)
	setSetting(t, c, "loki.retention-days", "2")
	runReconcile(t, r, "loki")
	if after := hashOf(t, c); before == "" || after == before {
		t.Fatalf("hash before %q after %q", before, after)
	}
}

func TestReconcileRemovesObjectsOfAnOldRelease(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	stale := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "loki-old"}}
	if err := c.Create(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	inventory := inventoryOf(testBundle.Addons["loki"])
	inventory[release.RefOf(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "monitoring", "name": "loki-old"}}})] = struct{}{}
	if err := writeInventory(context.Background(), c, "loki", inventory); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, "loki")
	if exists(c, &corev1.ConfigMap{}, "monitoring", "loki-old") {
		t.Fatal("the old object stayed")
	}
}

func TestKataPreflightFailedPrunes(t *testing.T) {
	r, c := enabledKataFixture(t)
	if !exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") {
		t.Fatal("kata was not applied")
	}
	setHostKVM(t, c, "w1", false)
	runReconcile(t, r, "kata")
	if exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") || exists(c, &nodev1.RuntimeClass{}, "", "kata") || exists(c, &rbacv1.ClusterRole{}, "", "kata-deploy-role") {
		t.Fatal("kata objects stayed")
	}
	if got := condition(t, c, "kata", "Ready"); got.Reason != "PreflightFailed" || got.Status != metav1.ConditionFalse {
		t.Errorf("ready %+v", got)
	}
	if got := condition(t, c, "kata", "Preflight"); got.Reason != "NoKVM" || got.Status != metav1.ConditionFalse {
		t.Errorf("preflight %+v", got)
	}
	if !getAddon(t, c, "kata").Spec.Enabled {
		t.Error("spec.enabled was rewritten")
	}
}

func TestKataEnablesWhenAKVMHostAppears(t *testing.T) {
	r, c := newFixture(t)
	addHost(t, c, "w1", v1alpha1.RoleWorkload, boolPtr(false), true)
	createAddon(t, c, "kata", true, "")
	runReconcile(t, r, "kata")
	if exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") {
		t.Fatal("kata applied without KVM")
	}
	addHost(t, c, "w2", v1alpha1.RoleWorkload, boolPtr(true), true)
	runReconcile(t, r, "kata")
	if !exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") {
		t.Fatal("kata was not applied")
	}
	if got := condition(t, c, "kata", "Preflight"); got.Reason != "Passed" || got.Status != metav1.ConditionTrue {
		t.Errorf("preflight %+v", got)
	}
}

func TestKataWaitsForUnknownHosts(t *testing.T) {
	r, c := newFixture(t)
	addHost(t, c, "w1", v1alpha1.RoleWorkload, nil, false)
	createAddon(t, c, "kata", true, "")
	runReconcile(t, r, "kata")
	if exists(c, &appsv1.DaemonSet{}, "kube-system", "kata-deploy") {
		t.Fatal("kata applied before the hosts reported")
	}
	if got := condition(t, c, "kata", "Ready"); got.Reason != "PreflightPending" {
		t.Errorf("ready %+v", got)
	}
	if got := condition(t, c, "kata", "Preflight"); got.Status != metav1.ConditionUnknown || got.Reason != "WaitingForHosts" {
		t.Errorf("preflight %+v", got)
	}
}

func TestReconcileWaitsForTheCluster(t *testing.T) {
	r, c := newFixture(t)
	setClusterVersion(t, c, "")
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	if got := condition(t, c, "loki", "Ready"); got.Reason != "WaitingForCluster" {
		t.Fatalf("ready %+v", got)
	}
	if exists(c, &appsv1.StatefulSet{}, "monitoring", "loki") {
		t.Fatal("applied before the cluster settled")
	}
}

func TestUnknownAddonIsReported(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "nothing", true, "")
	runReconcile(t, r, "nothing")
	if got := condition(t, c, "nothing", "Ready"); got.Reason != "UnknownAddon" {
		t.Fatalf("ready %+v", got)
	}
	addon := getAddon(t, c, "nothing")
	if len(addon.Finalizers) != 0 || exists(c, &corev1.ConfigMap{}, "bedrock-system", "bedrock-addon-nothing-inventory") {
		t.Fatalf("side effects: %+v", addon.Finalizers)
	}
}

type failingStatus struct{ client.SubResourceWriter }

func (failingStatus) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return apierrors.NewInternalError(context.DeadlineExceeded)
}

type failingStatusClient struct{ client.Client }

func (f failingStatusClient) Status() client.SubResourceWriter {
	return failingStatus{f.Client.Status()}
}

func TestReconcileSurvivesAStatusWriteFailure(t *testing.T) {
	r, c := newFixture(t)
	r.Client = failingStatusClient{c}
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	if !exists(c, &appsv1.StatefulSet{}, "monitoring", "loki") {
		t.Fatal("the objects were not applied")
	}
}

type failingDelete struct {
	client.Client
	name string
}

func (f failingDelete) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if obj.GetName() == f.name {
		return apierrors.NewInternalError(context.DeadlineExceeded)
	}
	return f.Client.Delete(ctx, obj, opts...)
}

func TestFailedPruneKeepsTheObjectRecorded(t *testing.T) {
	r, c := newFixture(t)
	createAddon(t, c, "loki", true, "")
	runReconcile(t, r, "loki")
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "loki-old"}}); err != nil {
		t.Fatal(err)
	}
	stale := release.RefOf(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "monitoring", "name": "loki-old"}}})
	recorded := inventoryOf(testBundle.Addons["loki"])
	recorded[stale] = struct{}{}
	if err := writeInventory(ctx, c, "loki", recorded); err != nil {
		t.Fatal(err)
	}
	r.Client = failingDelete{Client: c, name: "loki-old"}
	runReconcile(t, r, "loki")
	got, err := readInventory(ctx, c, "loki")
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := got[stale]; !kept {
		t.Fatal("the inventory forgot an object that was not deleted")
	}
	r.Client = c
	runReconcile(t, r, "loki")
	if exists(c, &corev1.ConfigMap{}, "monitoring", "loki-old") {
		t.Fatal("the next reconcile did not delete the stale object")
	}
	if got, _ := readInventory(ctx, c, "loki"); len(got) != len(testBundle.Addons["loki"]) {
		t.Fatalf("inventory %d objects", len(got))
	}
}
