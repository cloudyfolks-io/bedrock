package network

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type podPolicyFixture struct {
	alice    client.Client
	platform client.Client
	admin    client.Client
	ns       string
}

func impersonating(t *testing.T, cfg *rest.Config, user string, groups ...string) client.Client {
	t.Helper()
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	copied := rest.CopyConfig(cfg)
	copied.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
	c, err := client.New(copied, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func podFor(namespace, name string, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: annotations},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "registry.test/probe:1"}}},
	}
}

func withServiceAccount(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	err := c.Create(context.Background(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "default"}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func newPodPolicyFixture(t *testing.T) podPolicyFixture {
	t.Helper()
	c, cfg := testEnv(t)
	ns := newNamespace(t, unique(t, "pp"), "a")
	withServiceAccount(t, c, ns)
	f := podPolicyFixture{
		alice:    impersonating(t, cfg, "bedrock:alice", "system:masters"),
		platform: impersonating(t, cfg, "system:serviceaccount:kubevirt:kubevirt-controller", "system:masters"),
		admin:    c,
		ns:       ns,
	}
	probe := func() bool {
		pod := podFor(ns, unique(t, "probe"), map[string]string{"ovn.kubernetes.io/logical_switch": "x"})
		err := f.alice.Create(context.Background(), pod)
		if err == nil {
			_ = f.admin.Delete(context.Background(), pod)
		}
		return err != nil
	}
	installPolicy(t, c, "../../manifests/90-bedrock/network-pod-policy.yaml", probe)
	return f
}

const platformOnly = "only the platform may set network selection annotations"

func requirePodDenied(t *testing.T, c client.Client, pod *corev1.Pod) {
	t.Helper()
	err := c.Create(context.Background(), pod)
	if err == nil {
		t.Fatalf("pod %v must be denied", pod.Annotations)
	}
	if !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("error %q must contain %q", err, platformOnly)
	}
}

func requirePodAllowed(t *testing.T, c client.Client, pod *corev1.Pod) {
	t.Helper()
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatalf("pod %v must be allowed: %v", pod.Annotations, err)
	}
}

func TestPodPolicyDeniesNetworkSelectionByTenants(t *testing.T) {
	f := newPodPolicyFixture(t)
	for _, value := range []string{
		"pp-b/default",
		f.ns + "/default",
		"default",
		`[{"name":"default","namespace":"pp-b"}]`,
		`[{"name":"default","Namespace":"pp-b"}]`,
		`[{"name":"default","NAMESPACE":"pp-b"}]`,
		`[{"name":"default","namespac\u0065":"pp-b"}]`,
		`  [{"name":"default"}]`,
	} {
		requirePodDenied(t, f.alice, podFor(f.ns, unique(t, "n"), map[string]string{"k8s.v1.cni.cncf.io/networks": value}))
	}
	requirePodDenied(t, f.alice, podFor(f.ns, unique(t, "d"), map[string]string{"v1.multus-cni.io/default-network": "pp-b/x"}))
	requirePodDenied(t, f.alice, podFor(f.ns, unique(t, "s"), map[string]string{"k8s.v1.cni.cncf.io/network-status": "[]"}))
}

func TestPodPolicyDeniesFabricAnnotationsByTenants(t *testing.T) {
	f := newPodPolicyFixture(t)
	for _, key := range []string{
		"ovn.kubernetes.io/logical_switch", "ovn.kubernetes.io/security_groups",
		"fabric.cloudyfolks.io/logical_switch", "fabric.cloudyfolks.io/vpc",
		"default.pp-a.fabric.cloudyfolks.io/logical_switch", "default.pp-a.fabric.cloudyfolks.io/security_groups",
		"default.pp-a.fabric.cloudyfolks.io/port_security", "default.pp-a.fabric.cloudyfolks.io/mac_address",
		"default.pp-a.fabric.cloudyfolks.io/ip_address", "default.pp-a.provider-network.cloudyfolks.io/interface",
	} {
		requirePodDenied(t, f.alice, podFor(f.ns, unique(t, "p"), map[string]string{key: "x"}))
	}
}

func TestPodPolicyDeniesTenantControllers(t *testing.T) {
	f := newPodPolicyFixture(t)
	_, cfg := testEnv(t)
	for _, user := range []string{"system:serviceaccount:kube-system:replicaset-controller", "system:serviceaccount:kube-system:job-controller"} {
		c := impersonating(t, cfg, user, "system:masters")
		requirePodDenied(t, c, podFor(f.ns, unique(t, "rs"), map[string]string{"k8s.v1.cni.cncf.io/networks": "pp-b/default"}))
	}
}

func TestPodPolicyAllowsPlatformIdentities(t *testing.T) {
	f := newPodPolicyFixture(t)
	_, cfg := testEnv(t)
	annotations := map[string]string{"ovn.kubernetes.io/logical_switch": "s", "k8s.v1.cni.cncf.io/networks": "elsewhere/net", "x.cloudyfolks.io/ip_address": "10.0.0.1"}
	requirePodAllowed(t, f.platform, podFor(f.ns, unique(t, "kv"), annotations))
	for _, user := range []string{
		"system:serviceaccount:kube-system:ovn", "system:serviceaccount:kube-system:fabric-cni",
		"system:serviceaccount:kube-system:multus", "system:serviceaccount:bedrock-system:bedrock-operator",
	} {
		requirePodAllowed(t, impersonating(t, cfg, user, "system:masters"), podFor(f.ns, unique(t, "pl"), annotations))
	}
}

func TestPodPolicyDeniesChangesByTenantsOnUpdate(t *testing.T) {
	f := newPodPolicyFixture(t)
	ctx := context.Background()
	name := unique(t, "held")
	requirePodAllowed(t, f.platform, podFor(f.ns, name, map[string]string{"ovn.kubernetes.io/logical_switch": "s", "app": "x"}))
	get := func() corev1.Pod {
		var pod corev1.Pod
		if err := f.alice.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &pod); err != nil {
			t.Fatal(err)
		}
		return pod
	}
	relabeled := get()
	relabeled.Labels = map[string]string{"relabeled": "yes"}
	if err := f.alice.Update(ctx, &relabeled); err != nil {
		t.Fatalf("an unchanged annotation must be ignored: %v", err)
	}
	added := get()
	added.Annotations["k8s.v1.cni.cncf.io/networks"] = "pp-b/default"
	if err := f.alice.Update(ctx, &added); err == nil || !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("an added selection must be denied: %v", err)
	}
	changed := get()
	changed.Annotations["ovn.kubernetes.io/logical_switch"] = "other"
	if err := f.alice.Update(ctx, &changed); err == nil || !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("a changed selection must be denied: %v", err)
	}
	removed := get()
	delete(removed.Annotations, "ovn.kubernetes.io/logical_switch")
	if err := f.alice.Update(ctx, &removed); err == nil || !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("a removed selection must be denied: %v", err)
	}
	plain := get()
	plain.Annotations["app"] = "y"
	if err := f.alice.Update(ctx, &plain); err != nil {
		t.Fatalf("other annotations stay free: %v", err)
	}
	platform := get()
	platform.Annotations["ovn.kubernetes.io/logical_switch"] = "other"
	if err := f.platform.Update(ctx, &platform); err != nil {
		t.Fatalf("the platform may change the selection: %v", err)
	}
}

func TestPodPolicyIgnoresNamespacesWithoutTheTenantLabel(t *testing.T) {
	f := newPodPolicyFixture(t)
	bare := unique(t, "bare")
	if err := f.admin.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}}); err != nil {
		t.Fatal(err)
	}
	withServiceAccount(t, f.admin, bare)
	requirePodAllowed(t, f.alice, podFor(bare, unique(t, "p"), map[string]string{"ovn.kubernetes.io/logical_switch": "x", "k8s.v1.cni.cncf.io/networks": "other/net"}))
}

func TestPodPolicyAllowsPlainPods(t *testing.T) {
	f := newPodPolicyFixture(t)
	requirePodAllowed(t, f.alice, podFor(f.ns, unique(t, "plain"), nil))
	requirePodAllowed(t, f.alice, podFor(f.ns, unique(t, "labeled"), map[string]string{"app": "web", "prometheus.io/scrape": "true", "kubernetes.io/change-cause": "x"}))
}
