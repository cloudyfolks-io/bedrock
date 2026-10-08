package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/audit"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func putAuditPolicy(t *testing.T, policy string) *corev1.ConfigMap {
	t.Helper()
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}})); err != nil {
		t.Fatal(err)
	}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: audit.ConfigMapName}, Data: map[string]string{audit.PolicyFile: policy}}
	if err := k8sClient.Create(ctx, config); err != nil {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
			t.Fatal(err)
		}
		config.Data = map[string]string{audit.PolicyFile: policy}
		if err := k8sClient.Update(ctx, config); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), config) })
	return config
}

func removeAuditPolicy(t *testing.T) {
	t.Helper()
	err := k8sClient.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: audit.ConfigMapName}})
	if client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
}

func auditHost(role string) v1alpha1.Host {
	return hostWithRole("audit-host", role)
}

func policyPath(root string) string {
	return filepath.Join(root, audit.Dir, audit.PolicyFile)
}

func TestSyncAuditFilesWritesThePolicy(t *testing.T) {
	putAuditPolicy(t, "policy-v1")
	deps := authnDeps(t, time.Now())
	if err := syncAuditFiles(context.Background(), k8sClient, deps, auditHost(v1alpha1.RoleControlPlane)); err != nil {
		t.Fatal(err)
	}
	path := policyPath(deps.Root)
	if readFixtureFile(t, path) != "policy-v1" {
		t.Fatalf("policy %q", readFixtureFile(t, path))
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	past := time.Now().Add(-time.Hour)
	touch(t, path, past)
	if err := syncAuditFiles(context.Background(), k8sClient, deps, auditHost(v1alpha1.RoleControlPlane)); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(past.Truncate(time.Nanosecond)) && after.ModTime().After(past.Add(time.Second)) {
		t.Fatalf("an unchanged policy was rewritten: %v", after.ModTime())
	}
}

func TestSyncAuditFilesWaitsForTheConfigMap(t *testing.T) {
	removeAuditPolicy(t)
	deps := authnDeps(t, time.Now())
	err := syncAuditFiles(context.Background(), k8sClient, deps, auditHost(v1alpha1.RoleControlPlane))
	if err == nil || !strings.Contains(err.Error(), "waiting for "+audit.ConfigMapName) {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(policyPath(deps.Root)); statErr == nil {
		t.Fatal("a policy file was written")
	}
	if _, statErr := os.Stat(filepath.Join(deps.Root, audit.LogDir)); statErr == nil {
		t.Fatal("the log directory was created before the policy existed")
	}
	writeFixtureFile(t, policyPath(deps.Root), "older")
	if err := syncAuditFiles(context.Background(), k8sClient, deps, auditHost(v1alpha1.RoleControlPlane)); err == nil {
		t.Fatal("expected a waiting error")
	}
	if readFixtureFile(t, policyPath(deps.Root)) != "older" {
		t.Fatal("an older policy file must stay")
	}
}

func TestSyncAuditFilesCreatesTheLogDirectory(t *testing.T) {
	putAuditPolicy(t, "policy-v1")
	deps := authnDeps(t, time.Now())
	if err := syncAuditFiles(context.Background(), k8sClient, deps, auditHost(v1alpha1.RoleControlPlane)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(deps.Root, audit.LogDir))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("log directory %v %v", info, err)
	}
}

func TestSyncAuditFilesSkipsWorkers(t *testing.T) {
	putAuditPolicy(t, "policy-v1")
	deps := authnDeps(t, time.Now())
	if err := syncAuditFiles(context.Background(), k8sClient, deps, auditHost(v1alpha1.RoleWorkload)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(policyPath(deps.Root)); err == nil {
		t.Fatal("a worker got an audit policy")
	}
}

func TestWithAuditMessageAddsAPrefixedError(t *testing.T) {
	if got := withAuditMessage(nil, errors.New("x")); got != nil {
		t.Fatalf("got %+v", got)
	}
	status := &v1alpha1.AuthnFilesStatus{Message: "waiting for a"}
	got := withAuditMessage(status, errors.New("waiting for b"))
	if got.Message != "waiting for a; audit: waiting for b" || status.Message != "waiting for a" {
		t.Fatalf("got %q, input %q", got.Message, status.Message)
	}
	if clean := withAuditMessage(status, nil); clean != status {
		t.Fatal("no error must keep the status")
	}
}
