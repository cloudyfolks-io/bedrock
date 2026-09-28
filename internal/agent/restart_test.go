package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const platformNamespaceList = "bedrock-system cert-manager kubevirt rook-ceph traefik"

const workloadsJSON = `{"apiVersion":"v1","kind":"List","items":[
{"kind":"Deployment","metadata":{"name":"bedrock-operator","namespace":"bedrock-system"},"spec":{"strategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"bedrock-operator"}}}}},
{"kind":"Deployment","metadata":{"name":"rook-ceph-operator","namespace":"rook-ceph"},"spec":{"strategy":{"type":"Recreate"},"template":{"metadata":{"labels":{"app":"rook-ceph-operator"}}}}},
{"kind":"Deployment","metadata":{"name":"rook-ceph-mon-a","namespace":"rook-ceph"},"spec":{"strategy":{"type":"Recreate"},"template":{"metadata":{"labels":{"app":"rook-ceph-mon","ceph_daemon_type":"mon"}}}}},
{"kind":"Deployment","metadata":{"name":"rook-ceph-osd-0","namespace":"rook-ceph"},"spec":{"strategy":{"type":"Recreate"},"template":{"metadata":{"labels":{"app":"rook-ceph-osd","ceph_daemon_type":"osd"}}}}},
{"kind":"Deployment","metadata":{"name":"ovn-central","namespace":"kube-system"},"spec":{"strategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"ovn-central"}}}}},
{"kind":"Deployment","metadata":{"name":"traefik","namespace":"traefik"},"spec":{"strategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"traefik"}}}}},
{"kind":"Deployment","metadata":{"name":"web","namespace":"t-42"},"spec":{"strategy":{"type":"Recreate"},"template":{"metadata":{"labels":{"app":"web"}}}}},
{"kind":"DaemonSet","metadata":{"name":"virt-handler","namespace":"kubevirt"},"spec":{"updateStrategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"kubevirt.io":"virt-handler"}}}}},
{"kind":"DaemonSet","metadata":{"name":"fabric-cni","namespace":"kube-system"},"spec":{"updateStrategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"fabric-cni"}}}}},
{"kind":"DaemonSet","metadata":{"name":"ovs-ovn","namespace":"kube-system"},"spec":{"updateStrategy":{"type":"OnDelete"},"template":{"metadata":{"labels":{"app":"ovs"}}}}},
{"kind":"DaemonSet","metadata":{"name":"kube-vip","namespace":"kube-system"},"spec":{"updateStrategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"kube-vip"}}}}},
{"kind":"StatefulSet","metadata":{"name":"prometheus","namespace":"kube-system"},"spec":{"updateStrategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"prometheus"}}}}},
{"kind":"StatefulSet","metadata":{"name":"ovn-db","namespace":"kube-system"},"spec":{"updateStrategy":{"type":"OnDelete"},"template":{"metadata":{"labels":{"app":"ovn-db"}}}}},
{"kind":"StatefulSet","metadata":{"name":"cp","namespace":"t-42"},"spec":{"updateStrategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"k0smotron"}}}}}
]}`

var expectedRestarts = []string{
	"--namespace kube-system rollout restart daemonset/fabric-cni",
	"--namespace kubevirt rollout restart daemonset/virt-handler",
	"--namespace bedrock-system rollout restart deployment/bedrock-operator",
	"--namespace rook-ceph rollout restart deployment/rook-ceph-operator",
	"--namespace traefik rollout restart deployment/traefik",
	"--namespace kube-system rollout restart statefulset/prometheus",
}

type scriptedExec struct {
	fake        *host.FakeExec
	readyzCalls int
	readyAfter  int
}

func (s *scriptedExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	if slices.Contains(args, "/readyz") {
		s.readyzCalls++
		if s.readyzCalls <= s.readyAfter {
			return "", errors.New("connection refused")
		}
	}
	return s.fake.Run(ctx, name, args...)
}

func adminKubectlKey(root string, args ...string) string {
	return strings.Join(append([]string{"/usr/local/bin/k0s", "kubectl", "--kubeconfig", filepath.Join(root, "var/lib/k0s/pki/admin.conf")}, args...), " ")
}

func restoredClusterExec(root, workloads string) *host.FakeExec {
	responses := map[string]string{
		"systemctl stop k0scontroller.service":                                    "",
		"systemctl start k0scontroller.service":                                   "",
		adminKubectlKey(root, "--request-timeout=10s", "get", "--raw", "/readyz"): "ok",
		adminKubectlKey(root, "--request-timeout=30s", "get", "namespaces", "--selector", "bedrock.cloudyfolks.io/component", "-o", "jsonpath={.items[*].metadata.name}"): platformNamespaceList,
		adminKubectlKey(root, "--request-timeout=30s", "get", "deployments,daemonsets,statefulsets", "--all-namespaces", "-o", "json"):                                    workloads,
	}
	for _, restart := range expectedRestarts {
		responses[adminKubectlKey(root, append([]string{"--request-timeout=30s"}, strings.Fields(restart)...)...)] = ""
	}
	return &host.FakeExec{Responses: responses, ResponsePrefixes: map[string]string{"/usr/local/bin/k0s restore --config-out ": ""}}
}

func restartCalls(calls []string) []string {
	var restarts []string
	for _, call := range calls {
		if at := strings.Index(call, "--namespace "); at >= 0 && strings.Contains(call, " rollout restart ") {
			restarts = append(restarts, call[at:])
		}
	}
	return restarts
}

func restartDeps(t *testing.T) Deps {
	t.Helper()
	deps := newDeps(nil, time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
	deps.Root = t.TempDir()
	deps.K0sTimeout = time.Second
	deps.K0sPoll = time.Millisecond
	return deps
}

func TestRestoreRestartsThePlatformWorkloads(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	exec := restoredClusterExec(env.Deps.Root, workloadsJSON)
	env.Deps.Exec = exec
	outcome, err := restore(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got := restartCalls(exec.Calls); !slices.Equal(got, expectedRestarts) {
		t.Fatalf("restarts\n%v\nwant the kube-system DaemonSets first, no data plane and no tenant workload:\n%v", got, expectedRestarts)
	}
	if outcome.Message != "restored "+env.Upgrade.Spec.Backup+", workloads restarted: 6" {
		t.Fatalf("message %q", outcome.Message)
	}
}

func TestRestoreWaitsForTheAPIBeforeItRestarts(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	exec := &scriptedExec{fake: restoredClusterExec(env.Deps.Root, workloadsJSON), readyAfter: 2}
	env.Deps.Exec = exec
	if _, err := restore(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if exec.readyzCalls < 3 {
		t.Fatalf("readyz calls %d, want at least 3", exec.readyzCalls)
	}
	if got := restartCalls(exec.fake.Calls); len(got) != len(expectedRestarts) {
		t.Fatalf("restarts %v", got)
	}
}

func TestRestoreFailsWhenTheAPIStaysDown(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	exec := &scriptedExec{fake: restoredClusterExec(env.Deps.Root, workloadsJSON), readyAfter: 1 << 30}
	env.Deps.Exec = exec
	env.Deps.K0sTimeout = 30 * time.Millisecond
	_, err := restore(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "the API did not answer within 30ms") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error %v", err)
	}
	if got := restartCalls(exec.fake.Calls); len(got) != 0 {
		t.Fatalf("no restart without the API, got %v", got)
	}
}

func TestRestartTriesEveryWorkloadAndReportsTheFailures(t *testing.T) {
	deps := restartDeps(t)
	exec := restoredClusterExec(deps.Root, workloadsJSON)
	failing := adminKubectlKey(deps.Root, "--request-timeout=30s", "--namespace", "kubevirt", "rollout", "restart", "daemonset/virt-handler")
	exec.Errors = map[string]error{failing: errors.New("the server was unable to return a response in the time allotted")}
	deps.Exec = exec
	restarted, err := RestartPlatformWorkloads(context.Background(), deps)
	if err == nil || !strings.Contains(err.Error(), "restart DaemonSet kubevirt/virt-handler: the server was unable") {
		t.Fatalf("error %v", err)
	}
	if restarted != len(expectedRestarts)-1 {
		t.Fatalf("restarted %d, want %d", restarted, len(expectedRestarts)-1)
	}
	if got := restartCalls(exec.Calls); len(got) != len(expectedRestarts)+2 || got[len(got)-1] != expectedRestarts[len(expectedRestarts)-1] {
		t.Fatalf("a failed restart is tried %d times and the others still run: %v", restartAttempts, got)
	}
}

func TestRestartFailsOnABadWorkloadList(t *testing.T) {
	deps := restartDeps(t)
	exec := restoredClusterExec(deps.Root, "not json")
	deps.Exec = exec
	if _, err := RestartPlatformWorkloads(context.Background(), deps); err == nil || !strings.Contains(err.Error(), "workload list: ") {
		t.Fatalf("error %v", err)
	}
	if got := restartCalls(exec.Calls); len(got) != 0 {
		t.Fatalf("no restart without a list, got %v", got)
	}
}
