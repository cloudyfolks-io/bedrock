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
		pod := podFor(ns, unique(t, "probe"), map[string]string{"fabric.cloudyfolks.io/logical_switch": "x"})
		err := f.alice.Create(context.Background(), pod)
		if err == nil {
			_ = f.admin.Delete(context.Background(), pod)
		}
		return err != nil
	}
	installPolicy(t, c, "../../manifests/90-bedrock/network-pod-policy.yaml", probe)
	return f
}

func requirePodDenied(t *testing.T, c client.Client, pod *corev1.Pod, fragment string) {
	t.Helper()
	err := c.Create(context.Background(), pod)
	if err == nil {
		t.Fatalf("pod %v must be denied", pod.Annotations)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("error %q must contain %q", err, fragment)
	}
}

func requirePodAllowed(t *testing.T, c client.Client, pod *corev1.Pod) {
	t.Helper()
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatalf("pod %v must be allowed: %v", pod.Annotations, err)
	}
}

func TestPodPolicyDeniesAnAttachmentToAnotherNamespace(t *testing.T) {
	f := newPodPolicyFixture(t)
	const own = "a-own"
	denied := func(name string, annotations map[string]string) {
		requirePodDenied(t, f.alice, podFor(f.ns, unique(t, name), annotations), "a pod may attach only to networks of its own namespace")
	}
	denied("a", map[string]string{"k8s.v1.cni.cncf.io/networks": "pp-b/default"})
	denied("b", map[string]string{"k8s.v1.cni.cncf.io/networks": `[{"name":"default","namespace":"pp-b"}]`})
	denied("c", map[string]string{"v1.multus-cni.io/default-network": "pp-b/x"})
	denied("d", map[string]string{"k8s.v1.cni.cncf.io/networks": f.ns + "/default,other/x"})
	for _, value := range []string{f.ns + "/default", "default", f.ns + "/default@net1"} {
		requirePodAllowed(t, f.alice, podFor(f.ns, unique(t, own), map[string]string{"k8s.v1.cni.cncf.io/networks": value}))
	}
}

func TestPodPolicyDeniesFabricSubnetAndSecurityGroupSelection(t *testing.T) {
	f := newPodPolicyFixture(t)
	for _, key := range []string{
		"fabric.cloudyfolks.io/logical_switch", "default.pp-a.fabric.cloudyfolks.io/logical_switch",
		"default.pp-a.fabric.cloudyfolks.io/security_groups", "fabric.cloudyfolks.io/ip_pool",
		"fabric.cloudyfolks.io/logical_router", "fabric.cloudyfolks.io/vpc",
	} {
		requirePodDenied(t, f.alice, podFor(f.ns, unique(t, "p"), map[string]string{key: "x"}), "only the platform may set it")
	}
	requirePodAllowed(t, f.alice, podFor(f.ns, unique(t, "ok"), map[string]string{"default.pp-a.fabric.cloudyfolks.io/port_security": "true"}))
}

func TestPodPolicyAllowsTrustedRequesters(t *testing.T) {
	f := newPodPolicyFixture(t)
	annotations := map[string]string{"fabric.cloudyfolks.io/logical_switch": "s", "k8s.v1.cni.cncf.io/networks": "elsewhere/net"}
	requirePodAllowed(t, f.platform, podFor(f.ns, unique(t, "kv"), annotations))
	_, cfg := testEnv(t)
	ovn := impersonating(t, cfg, "system:serviceaccount:kube-system:ovn", "system:masters")
	requirePodAllowed(t, ovn, podFor(f.ns, unique(t, "ovn"), annotations))
}

func TestPodPolicyIgnoresUnchangedAnnotationsOnUpdate(t *testing.T) {
	f := newPodPolicyFixture(t)
	ctx := context.Background()
	name := unique(t, "held")
	requirePodAllowed(t, f.platform, podFor(f.ns, name, map[string]string{"fabric.cloudyfolks.io/logical_switch": "s"}))
	var pod corev1.Pod
	if err := f.alice.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels = map[string]string{"relabeled": "yes"}
	if err := f.alice.Update(ctx, &pod); err != nil {
		t.Fatalf("an unchanged annotation must be ignored: %v", err)
	}
	pod.Annotations["fabric.cloudyfolks.io/security_groups"] = "other"
	err := f.alice.Update(ctx, &pod)
	if err == nil || !strings.Contains(err.Error(), "only the platform may set it") {
		t.Fatalf("a new forbidden annotation must be denied: %v", err)
	}
}

func TestPodPolicyIgnoresNamespacesWithoutTheTenantLabel(t *testing.T) {
	f := newPodPolicyFixture(t)
	bare := unique(t, "bare")
	if err := f.admin.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}}); err != nil {
		t.Fatal(err)
	}
	withServiceAccount(t, f.admin, bare)
	requirePodAllowed(t, f.alice, podFor(bare, unique(t, "p"), map[string]string{"fabric.cloudyfolks.io/logical_switch": "x", "k8s.v1.cni.cncf.io/networks": "other/net"}))
}

func TestPodPolicyAllowsPlainPods(t *testing.T) {
	f := newPodPolicyFixture(t)
	requirePodAllowed(t, f.alice, podFor(f.ns, unique(t, "plain"), nil))
	requirePodAllowed(t, f.alice, podFor(f.ns, unique(t, "labeled"), map[string]string{"app": "web"}))
}
