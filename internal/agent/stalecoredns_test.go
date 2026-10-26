package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func staleCoreDNSDir(root string) string {
	return filepath.Join(root, "var/lib/k0s/manifests/coredns")
}

func pruneEnvWithStaleDir(t *testing.T) StepEnv {
	t.Helper()
	env, _ := finishEnv(t, nil)
	env.Client = k8sClient
	writeFixtureFile(t, filepath.Join(staleCoreDNSDir(env.Deps.Root), "coredns.yaml"), "kind: Deployment\n")
	return env
}

func markTakeoverDone(t *testing.T) {
	t.Helper()
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName, Annotations: map[string]string{v1alpha1.AnnotationK0sCoreDNSTakeover: "done"}}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v0.3.0", API: v1alpha1.APISpec{VIP: "10.0.0.10"}}}
	if err := k8sClient.Create(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), cluster) })
}

func podTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"k8s-app": "kube-dns"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "coredns", Image: "coredns:test"}}},
	}
}

func labeledDeployment(t *testing.T, stack string) {
	t.Helper()
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns", Labels: map[string]string{"k0s.k0sproject.io/stack": stack}},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
			Template: podTemplate(),
		},
	}
	if err := k8sClient.Create(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), deployment) })
}

func TestPruneRemovesTheStaleK0sCoreDNSManifestsAfterTheTakeover(t *testing.T) {
	env := pruneEnvWithStaleDir(t)
	markTakeoverDone(t)
	outcome, err := prune(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleCoreDNSDir(env.Deps.Root)); !os.IsNotExist(err) {
		t.Fatalf("the stale manifest dir must go: %v %+v", err, outcome)
	}
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatalf("a second run must do nothing: %v", err)
	}
}

func TestPruneKeepsTheStaleK0sCoreDNSManifestsBeforeTheTakeover(t *testing.T) {
	env := pruneEnvWithStaleDir(t)
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "v0.3.0", API: v1alpha1.APISpec{VIP: "10.0.0.10"}}}
	if err := k8sClient.Create(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), cluster) })
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleCoreDNSDir(env.Deps.Root)); err != nil {
		t.Fatalf("without the takeover mark the dir must stay: %v", err)
	}
}

func TestPruneRefusesToRemoveTheManifestsWhileK0sObjectsKeepTheStackLabel(t *testing.T) {
	env := pruneEnvWithStaleDir(t)
	markTakeoverDone(t)
	labeledDeployment(t, "coredns")
	_, err := prune(context.Background(), env)
	if err == nil {
		t.Fatal("removing the dir while the label is on an object would make k0s delete the object")
	}
	for _, part := range []string{"Deployment kube-system/coredns", "kubectl -n kube-system label deployment coredns k0s.k0sproject.io/stack-", "Prune"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("the message must name the repair, missing %q: %v", part, err)
		}
	}
	if _, err := os.Stat(staleCoreDNSDir(env.Deps.Root)); err != nil {
		t.Fatalf("the dir must stay: %v", err)
	}
}

func TestPruneIgnoresObjectsOfOtherK0sStacks(t *testing.T) {
	env := pruneEnvWithStaleDir(t)
	markTakeoverDone(t)
	labeledDeployment(t, "metrics")
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleCoreDNSDir(env.Deps.Root)); !os.IsNotExist(err) {
		t.Fatalf("the dir must go: %v", err)
	}
}

func TestPruneWithoutTheStaleDirNeedsNoCluster(t *testing.T) {
	env, _ := finishEnv(t, nil)
	env.Client = k8sClient
	if _, err := prune(context.Background(), env); err != nil {
		t.Fatal(err)
	}
}

func TestRepairCommandOfAClusterScopedObject(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "system-coredns"}}}
	object.SetKind("ClusterRole")
	if got := labelRepairCommand(object); got != "kubectl label clusterrole system-coredns k0s.k0sproject.io/stack-" {
		t.Fatalf("command %q", got)
	}
}
