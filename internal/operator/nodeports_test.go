package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func k0sClusterConfig(proxy map[string]any) *unstructured.Unstructured {
	network := map[string]any{"provider": "custom"}
	if proxy != nil {
		network["kubeProxy"] = proxy
	}
	object := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"network": network}}}
	object.SetGroupVersionKind(k0sClusterConfigGVK)
	object.SetNamespace(k0sConfigNamespace)
	object.SetName(k0sConfigName)
	return object
}

func nodePortReconciler(t *testing.T, cidr string, objects ...client.Object) (*ClusterReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{ManagementCIDR: cidr}}
	objects = append(objects, cluster)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(cluster).Build()
	return &ClusterReconciler{Client: c, APIReader: c}, c
}

func liveKubeProxy(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(k0sClusterConfigGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: k0sConfigNamespace, Name: k0sConfigName}, live); err != nil {
		t.Fatal(err)
	}
	proxy, _, _ := unstructured.NestedMap(live.Object, "spec", "network", "kubeProxy")
	return proxy
}

func clusterConditionOf(t *testing.T, c client.Client, conditionType string) *metav1.Condition {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(cluster.Status.Conditions, conditionType)
}

func nodePortCondition(t *testing.T, c client.Client) *metav1.Condition {
	t.Helper()
	return clusterConditionOf(t, c, v1alpha1.ConditionNodePortsScoped)
}

func TestNodePortScopePatchesOnlyTheAddresses(t *testing.T) {
	admin := map[string]any{"mode": "ipvs", "metricsBindAddress": "10.0.10.5:10249"}
	r, c := nodePortReconciler(t, "10.0.10.0/24", k0sClusterConfig(admin))
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	proxy := liveKubeProxy(t, c)
	addresses, _ := proxy["nodePortAddresses"].([]any)
	if len(addresses) != 1 || addresses[0] != "10.0.10.0/24" {
		t.Fatalf("kubeProxy %v", proxy)
	}
	if proxy["mode"] != "ipvs" || proxy["metricsBindAddress"] != "10.0.10.5:10249" {
		t.Fatalf("admin fields must stay: %v", proxy)
	}
	if got := nodePortCondition(t, c); got == nil || got.Status != metav1.ConditionTrue || got.Reason != "Scoped" {
		t.Fatalf("condition %+v", got)
	}
}

func TestNodePortScopeAddsTheBlockWhenAbsent(t *testing.T) {
	r, c := nodePortReconciler(t, "10.0.10.0/24", k0sClusterConfig(nil))
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if addresses, _ := liveKubeProxy(t, c)["nodePortAddresses"].([]any); len(addresses) != 1 {
		t.Fatalf("kubeProxy %v", liveKubeProxy(t, c))
	}
}

func TestNodePortScopeKeepsAnAdminChoice(t *testing.T) {
	custom := map[string]any{"nodePortAddresses": []any{"192.168.0.0/16"}}
	r, c := nodePortReconciler(t, "10.0.10.0/24", k0sClusterConfig(custom))
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if addresses, _ := liveKubeProxy(t, c)["nodePortAddresses"].([]any); addresses[0] != "192.168.0.0/16" {
		t.Fatalf("kubeProxy %v", liveKubeProxy(t, c))
	}
	disabled := k0sClusterConfig(map[string]any{"disabled": true})
	r, c = nodePortReconciler(t, "10.0.10.0/24", disabled)
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := liveKubeProxy(t, c)["nodePortAddresses"]; found {
		t.Fatal("a disabled kube-proxy must stay untouched")
	}
}

func TestNodePortScopeReportsAMissingCIDR(t *testing.T) {
	r, c := nodePortReconciler(t, "", k0sClusterConfig(nil))
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := nodePortCondition(t, c)
	if got == nil || got.Status != metav1.ConditionFalse || got.Reason != "NoManagementCIDR" {
		t.Fatalf("condition %+v", got)
	}
	if _, found := liveKubeProxy(t, c)["nodePortAddresses"]; found {
		t.Fatal("nothing may be patched without a CIDR")
	}
}

func TestNodePortScopeReportsAMissingK0sConfig(t *testing.T) {
	r, c := nodePortReconciler(t, "10.0.10.0/24")
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatalf("a missing k0s ClusterConfig is reported, not retried: %v", err)
	}
	got := nodePortCondition(t, c)
	if got == nil || got.Status != metav1.ConditionFalse || got.Reason != "K0sConfigUnavailable" {
		t.Fatalf("condition %+v", got)
	}
}

type patchIgnored struct{ client.Client }

func (patchIgnored) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return nil
}

func TestNodePortScopeVerifiesTheLiveObject(t *testing.T) {
	r, c := nodePortReconciler(t, "10.0.10.0/24", k0sClusterConfig(nil))
	r.Client = patchIgnored{c}
	if err := r.reconcileNodePorts(context.Background()); err == nil {
		t.Fatal("a patch that did not land must fail")
	}
	got := nodePortCondition(t, c)
	if got == nil || got.Status != metav1.ConditionFalse || got.Reason != "NotApplied" {
		t.Fatalf("condition %+v", got)
	}
}

type patchDenied struct{ client.Client }

func (patchDenied) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return errors.New("forbidden")
}

func TestNodePortScopeReportsAFailedPatch(t *testing.T) {
	r, c := nodePortReconciler(t, "10.0.10.0/24", k0sClusterConfig(nil))
	r.Client = patchDenied{c}
	if err := r.reconcileNodePorts(context.Background()); err == nil {
		t.Fatal("a denied patch must fail")
	}
	if got := nodePortCondition(t, c); got == nil || got.Status != metav1.ConditionFalse || got.Reason != "PatchFailed" {
		t.Fatalf("condition %+v", got)
	}
}

func TestOperatorRolePatchesTheK0sClusterConfig(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var committed rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &committed); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"get", "patch"} {
		if !allows(role, "k0s.k0sproject.io", "clusterconfigs", verb) || !allows(committed, "k0s.k0sproject.io", "clusterconfigs", verb) {
			t.Errorf("both operator roles must allow %s on k0s clusterconfigs", verb)
		}
	}
	if allows(role, "k0s.k0sproject.io", "clusterconfigs", "delete") {
		t.Error("the operator must not delete the k0s ClusterConfig")
	}
}

func TestNodePortScopeReportsAMissingK0sKind(t *testing.T) {
	r, c := nodePortReconciler(t, "10.0.10.0/24")
	r.APIReader = noKindReader{c}
	if err := r.reconcileNodePorts(context.Background()); err != nil {
		t.Fatalf("an absent kind is reported, not retried: %v", err)
	}
	if got := nodePortCondition(t, c); got == nil || got.Reason != "K0sConfigUnavailable" {
		t.Fatalf("condition %+v", got)
	}
}

type noKindReader struct{ client.Client }

func (r noKindReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isCluster := obj.(*v1alpha1.Cluster); isCluster {
		return r.Client.Get(ctx, key, obj, opts...)
	}
	return &meta.NoKindMatchError{GroupKind: k0sClusterConfigGVK.GroupKind()}
}
