package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/agent"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func adminKubectlCall(root string, args ...string) string {
	return strings.Join(append([]string{"/usr/local/bin/k0s", "kubectl", "--kubeconfig", filepath.Join(root, "var/lib/k0s/pki/admin.conf")}, args...), " ")
}

func restartWorkloadsExec(root string) *host.FakeExec {
	return &host.FakeExec{Responses: map[string]string{
		adminKubectlCall(root, "--request-timeout=10s", "get", "--raw", "/readyz"):                                                                                         "ok",
		adminKubectlCall(root, "--request-timeout=30s", "get", "namespaces", "--selector", "bedrock.cloudyfolks.io/component", "-o", "jsonpath={.items[*].metadata.name}"): "bedrock-system",
		adminKubectlCall(root, "--request-timeout=30s", "get", "deployments,daemonsets,statefulsets", "--all-namespaces", "-o", "json"): `{"items":[
{"kind":"Deployment","metadata":{"name":"bedrock-operator","namespace":"bedrock-system"},"spec":{"template":{"metadata":{"labels":{"app":"bedrock-operator"}}}}},
{"kind":"DaemonSet","metadata":{"name":"fabric-cni","namespace":"kube-system"},"spec":{"updateStrategy":{"type":"RollingUpdate"},"template":{"metadata":{"labels":{"app":"fabric-cni"}}}}}]}`,
		adminKubectlCall(root, "--request-timeout=30s", "--namespace", "kube-system", "rollout", "restart", "daemonset/fabric-cni"):           "",
		adminKubectlCall(root, "--request-timeout=30s", "--namespace", "bedrock-system", "rollout", "restart", "deployment/bedrock-operator"): "",
	}}
}

func restartWorkloadsDeps(root string, exec host.Exec) agent.Deps {
	return agent.Deps{Exec: exec, Root: root, K0sTimeout: time.Second, K0sPoll: time.Millisecond, ProbeTimeout: time.Second}
}

func TestRestartWorkloadsRestartsThePlatform(t *testing.T) {
	root := t.TempDir()
	exec := restartWorkloadsExec(root)
	var stdout, stderr bytes.Buffer
	if code := restartWorkloads(context.Background(), restartWorkloadsDeps(root, exec), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if stdout.String() != "workloads restarted: 2\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestRestartWorkloadsReportsFailures(t *testing.T) {
	root := t.TempDir()
	exec := restartWorkloadsExec(root)
	exec.Errors = map[string]error{adminKubectlCall(root, "--request-timeout=30s", "--namespace", "kube-system", "rollout", "restart", "daemonset/fabric-cni"): errors.New("exit status 1")}
	var stdout, stderr bytes.Buffer
	if code := restartWorkloads(context.Background(), restartWorkloadsDeps(root, exec), &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if stdout.String() != "workloads restarted: 1\n" || !strings.Contains(stderr.String(), "restart DaemonSet kube-system/fabric-cni: exit status 1") {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestRestartWorkloadsIsACommand(t *testing.T) {
	if _, ok := Commands()["restart-workloads"]; !ok {
		t.Fatal("bedrock restart-workloads must be a command, the restore runbook uses it")
	}
}
