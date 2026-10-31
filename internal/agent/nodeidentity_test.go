package agent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/hostconfig"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const identityNode = "identity-node"

type forbiddenRecorder struct {
	next http.RoundTripper
	t    *testing.T
}

func (r forbiddenRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusForbidden {
		r.t.Errorf("node identity is forbidden: %s %s", req.Method, req.URL.Path)
	}
	return resp, err
}

func applyShippedAgentRBAC(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	system := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}
	if err := k8sClient.Create(ctx, system); client.IgnoreAlreadyExists(err) != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "manifests", "90-bedrock", "agent*-rbac.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("agent rbac manifests: %v %v", files, err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(raw), "\n---\n") {
			object := &unstructured.Unstructured{}
			if err := yaml.Unmarshal([]byte(doc), &object.Object); err != nil {
				t.Fatal(err)
			}
			if err := k8sClient.Create(ctx, object); client.IgnoreAlreadyExists(err) != nil {
				t.Fatalf("%s: %v", file, err)
			}
		}
	}
}

func grantNodeAuthorizerEquivalent(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": "test-node-authorizer"},
		"rules":    []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"nodes"}, "verbs": []any{"get", "list", "watch"}}},
	}}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "test-node-authorizer"},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "test-node-authorizer"},
		"subjects": []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "system:node:" + identityNode}},
	}}
	for _, object := range []client.Object{role, binding} {
		if err := k8sClient.Create(ctx, object); client.IgnoreAlreadyExists(err) != nil {
			t.Fatal(err)
		}
	}
}

func nodeIdentityRESTClient(t *testing.T) client.Client {
	t.Helper()
	conf := rest.CopyConfig(cfg)
	conf.Impersonate = rest.ImpersonationConfig{UserName: "system:node:" + identityNode, Groups: []string{"system:nodes", "system:authenticated"}}
	conf.Wrap(func(next http.RoundTripper) http.RoundTripper { return forbiddenRecorder{next: next, t: t} })
	c, err := client.New(conf, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAgentCallsStayWithinTheRightsOfTheNodeIdentity(t *testing.T) {
	ctx := context.Background()
	applyShippedAgentRBAC(t)
	grantNodeAuthorizerEquivalent(t)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: identityNode}}
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, node) })
	nodeClient := nodeIdentityRESTClient(t)

	env, fake := k0sStepEnv(t, identityNode, v1alpha1.RoleControlPlane)
	own := getHost(t, identityNode)
	own.Spec.Management.Enabled = true
	if err := k8sClient.Update(ctx, &own); err != nil {
		t.Fatal(err)
	}
	hostConfig := &v1alpha1.HostConfig{ObjectMeta: metav1.ObjectMeta{Name: identityNode}}
	if err := k8sClient.Create(ctx, hostConfig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, hostConfig) })
	createReleases(t)
	createUpgrade(t, identityNode, v1alpha1.StepPreload)

	deps := newDeps(&host.FakeExec{}, time.Now())
	deps.Node = identityNode
	deps.Root = t.TempDir()
	deps.Apply = func(context.Context, hostconfig.Deps, v1alpha1.HostConfigSpec) []v1alpha1.StepResult { return nil }
	deps.Guard = func(context.Context, hostconfig.Deps, v1alpha1.HostConfigSpec) error { return nil }

	t.Run("K0sReconfigure reads the kubelet settings", func(t *testing.T) {
		writeFixtureFile(t, filepath.Join(env.Deps.Root, "usr/local/bin/k0s"), "new k0s")
		fake.running = newK0s
		writeFixtureFile(t, filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml"), "apiVersion: k0s.k0sproject.io/v1beta1\n")
		writeFixtureFile(t, controllerUnitPath(env.Deps.Root), controllerUnit)
		env.Client = nodeClient
		if _, err := k0sReconfigure(ctx, env); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Tick reads own Host and HostConfig and writes the Host status", func(t *testing.T) {
		if err := Tick(ctx, nodeClient, deps); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("RunUpgrades lists own NodeUpgrades, reads Releases, writes NodeUpgrade status", func(t *testing.T) {
		noop := func(context.Context, StepEnv) (Outcome, error) { return Outcome{Message: "done"}, nil }
		if err := RunUpgrades(ctx, nodeClient, deps, map[string]Step{v1alpha1.StepPreload: noop}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Prune checks stale CoreDNS with the admin client", func(t *testing.T) {
		markTakeoverDone(t)
		writeFixtureFile(t, filepath.Join(staleCoreDNSDir(deps.Root), "coredns.yaml"), "kind: Deployment\n")
		adminUsed := false
		deps.AdminClient = func(string) (client.Client, error) { adminUsed = true; return k8sClient, nil }
		if err := removeStaleCoreDNSManifests(ctx, StepEnv{Deps: deps, Client: nodeClient}); err != nil {
			t.Fatal(err)
		}
		if !adminUsed {
			t.Fatal("the stale CoreDNS check must use the admin client")
		}
	})
	t.Run("backup schedule and archive recipients are read", func(t *testing.T) {
		if _, err := etcdSchedule(ctx, nodeClient); err != nil {
			t.Fatal(err)
		}
		if _, err := archiveRecipientsValue(ctx, nodeClient); err != nil {
			t.Fatal(err)
		}
		if err := tidyBackupDir(ctx, nodeClient, deps); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("image sync lists Releases, adoption lists own NodeUpgrades", func(t *testing.T) {
		releaseLists(ctx, nodeClient, deps.Root)
		legacyAirgapVersion(ctx, nodeClient, deps)
	})
	t.Run("authn and encryption steps read own Host and NodeUpgrades", func(t *testing.T) {
		if err := EnableAuthnArgs(ctx, nodeClient, deps); err != nil {
			t.Fatal(err)
		}
		if err := ApplyEncryption(ctx, nodeClient, deps, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("defrag lists Hosts", func(t *testing.T) {
		if err := RunEtcdDefrag(ctx, nodeClient, deps); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("depot status reads the own Node", func(t *testing.T) {
		if _, err := NodeInternalIP(ctx, nodeClient, identityNode); err == nil || apierrors.IsForbidden(err) {
			t.Fatalf("a Node without address must fail with a plain error, got %v", err)
		}
	})
	t.Run("etcd ship", func(t *testing.T) {
		t.Skip("ship reads backup.target, backup.credentials-secret and a Secret in bedrock-system that no agent RBAC grants; tracked as a separate gap")
	})
}
