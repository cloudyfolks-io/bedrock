package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func nodePortEnv(t *testing.T, cluster *v1alpha1.Cluster, exec *host.FakeExec) StepEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if cluster != nil {
		builder = builder.WithObjects(cluster)
	}
	return StepEnv{Client: builder.Build(), Deps: Deps{Root: t.TempDir(), Exec: exec}}
}

func kubeProxyOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := sigyaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	proxy, _ := doc["spec"].(map[string]any)["network"].(map[string]any)["kubeProxy"].(map[string]any)
	return proxy
}

func TestEnsureNodePortScopeUsesTheClusterCIDR(t *testing.T) {
	cluster := &v1alpha1.Cluster{}
	cluster.Name = v1alpha1.ClusterName
	cluster.Spec.ManagementCIDR = "10.0.10.0/24"
	env := nodePortEnv(t, cluster, &host.FakeExec{})
	path := filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, path, plainConfig)
	changed, err := ensureNodePortScope(context.Background(), env, controllerService())
	if err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	proxy := kubeProxyOf(t, readFixtureFile(t, path))
	if proxy["mode"] != "iptables" || proxy["metricsBindAddress"] != "0.0.0.0:10249" || proxy["healthzBindAddress"] != "0.0.0.0:10256" {
		t.Fatalf("kubeProxy %v", proxy)
	}
	if addresses := proxy["nodePortAddresses"].([]any); len(addresses) != 1 || addresses[0] != "10.0.10.0/24" {
		t.Fatalf("nodePortAddresses %v", addresses)
	}
	if args := extraArgsOf(t, []byte(readFixtureFile(t, path))); args["default-not-ready-toleration-seconds"] != "30" {
		t.Fatalf("existing config must stay: %v", args)
	}
}

func TestEnsureNodePortScopeFallsBackToTheDefaultInterface(t *testing.T) {
	exec := &host.FakeExec{Responses: map[string]string{
		"ip -json route show default":        `[{"dst":"default","dev":"bond0.10"}]`,
		"ip -json -4 addr show dev bond0.10": `[{"addr_info":[{"family":"inet","local":"10.0.10.11","prefixlen":24}]}]`,
	}}
	env := nodePortEnv(t, nil, exec)
	path := filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, path, plainConfig)
	if _, err := ensureNodePortScope(context.Background(), env, controllerService()); err != nil {
		t.Fatal(err)
	}
	if addresses := kubeProxyOf(t, readFixtureFile(t, path))["nodePortAddresses"].([]any); addresses[0] != "10.0.10.0/24" {
		t.Fatalf("nodePortAddresses %v", addresses)
	}
}

func TestEnsureNodePortScopeIsIdempotent(t *testing.T) {
	cluster := &v1alpha1.Cluster{}
	cluster.Name = v1alpha1.ClusterName
	cluster.Spec.ManagementCIDR = "10.0.10.0/24"
	env := nodePortEnv(t, cluster, &host.FakeExec{})
	path := filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml")
	writeFixtureFile(t, path, plainConfig)
	if changed, err := ensureNodePortScope(context.Background(), env, controllerService()); err != nil || !changed {
		t.Fatalf("first run %v %v", changed, err)
	}
	first := readFixtureFile(t, path)
	if changed, err := ensureNodePortScope(context.Background(), env, controllerService()); err != nil || changed || readFixtureFile(t, path) != first {
		t.Fatalf("second run %v %v", changed, err)
	}
}

func TestEnsureNodePortScopeKeepsAnAdminChoice(t *testing.T) {
	env := nodePortEnv(t, nil, &host.FakeExec{})
	path := filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml")
	custom := "apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\nspec:\n  network:\n    kubeProxy:\n      nodePortAddresses:\n      - 192.168.0.0/16\n"
	writeFixtureFile(t, path, custom)
	if changed, err := ensureNodePortScope(context.Background(), env, controllerService()); err != nil || changed || readFixtureFile(t, path) != custom {
		t.Fatalf("changed %v err %v", changed, err)
	}
}

func TestEnsureNodePortScopeLeavesWorkersAndMissingConfig(t *testing.T) {
	env := nodePortEnv(t, nil, &host.FakeExec{})
	if changed, err := ensureNodePortScope(context.Background(), env, controllerService()); err != nil || changed {
		t.Fatalf("no k0s.yaml: %v %v", changed, err)
	}
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml"), plainConfig)
	if changed, err := ensureNodePortScope(context.Background(), env, k0sServiceOf(hostWithRole("w", v1alpha1.RoleWorkload))); err != nil || changed {
		t.Fatalf("worker: %v %v", changed, err)
	}
}

func TestEnsureNodePortScopeFailsWhenTheNetworkIsUnknown(t *testing.T) {
	env := nodePortEnv(t, nil, &host.FakeExec{})
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml"), plainConfig)
	_, err := ensureNodePortScope(context.Background(), env, controllerService())
	if err == nil || !strings.Contains(err.Error(), "management network") {
		t.Fatalf("err %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(env.Deps.Root, "etc/k0s/k0s.yaml")); statErr != nil {
		t.Fatal(statErr)
	}
}
