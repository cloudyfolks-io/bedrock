package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func renderedAuthn(t *testing.T, platformHost string) (authentication, webhook []byte) {
	t.Helper()
	in := apiserver.Inputs{Host: platformHost, CA: []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"), Bearer: "bearer-1"}
	authentication, err := apiserver.AuthenticationConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	webhook, err = apiserver.WebhookKubeconfig(in)
	if err != nil {
		t.Fatal(err)
	}
	return authentication, webhook
}

func putAuthnSources(t *testing.T, platformHost string) (authentication, webhook []byte) {
	t.Helper()
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}})); err != nil {
		t.Fatal(err)
	}
	authentication, webhook = renderedAuthn(t, platformHost)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: apiserver.ConfigMapName}, Data: map[string]string{apiserver.AuthenticationFile: string(authentication)}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: apiserver.SecretName}, Data: map[string][]byte{apiserver.WebhookFile: webhook}}
	for _, obj := range []client.Object{cm, secret} {
		err := k8sClient.Create(ctx, obj)
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatal(err)
		}
		if err != nil {
			if err := k8sClient.Update(ctx, obj); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		k8sClient.Delete(context.Background(), cm)
		k8sClient.Delete(context.Background(), secret)
	})
	return authentication, webhook
}

func hostWithRole(name, role string) v1alpha1.Host {
	return v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{role}}}
}

func authnDeps(t *testing.T, now time.Time) Deps {
	t.Helper()
	deps := newDeps(&host.FakeExec{}, now)
	deps.Root = t.TempDir()
	return deps
}

func fakeAPIServer(t *testing.T, root string, start time.Time) {
	t.Helper()
	binary := filepath.Join(root, "var", "lib", "k0s", "bin", "kube-apiserver")
	writeFixtureFile(t, binary, "kube-apiserver")
	writeFixtureFile(t, filepath.Join(root, "proc", "1", "comm"), "systemd\n")
	writeFixtureFile(t, filepath.Join(root, "proc", "1", "stat"), "1 (systemd) S "+strings.Repeat("0 ", 18)+"1\n")
	fakeProcess(t, root, 4242, start, binary)
}

func fakeTenantAPIServer(t *testing.T, root string, pid int, start time.Time) {
	t.Helper()
	binary := filepath.Join(root, "tenant", strconv.Itoa(pid), "kube-apiserver")
	writeFixtureFile(t, binary, "kube-apiserver")
	fakeProcess(t, root, pid, start, binary)
}

func fakeProcess(t *testing.T, root string, pid int, start time.Time, exe string) {
	t.Helper()
	boot := time.Unix(946684800, 0)
	ticks := int64(start.Sub(boot) / (10 * time.Millisecond))
	dir := filepath.Join(root, "proc", strconv.Itoa(pid))
	writeFixtureFile(t, filepath.Join(root, "proc", "stat"), fmt.Sprintf("cpu 1 2 3 4\nbtime %d\nprocesses 9\n", boot.Unix()))
	writeFixtureFile(t, filepath.Join(dir, "comm"), "kube-apiserver\n")
	writeFixtureFile(t, filepath.Join(dir, "stat"), fmt.Sprintf("%d (kube-apiserver) S %s%d 0 0\n", pid, strings.Repeat("0 ", 18), ticks))
	if err := os.RemoveAll(filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
}

func TestSyncWritesFilesWithMode0600(t *testing.T) {
	authentication, webhook := putAuthnSources(t, "lab.example")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	deps := authnDeps(t, now)
	var owned []string
	deps.AuthnOwner = func(path string) error {
		owned = append(owned, path)
		return nil
	}
	status := syncAuthnFiles(context.Background(), k8sClient, deps, hostWithRole("cp-write", v1alpha1.RoleControlPlane))
	if status == nil || status.Message != "" || status.Hash != apiserver.Hash(authentication, webhook) || status.WrittenAt == nil || !status.WrittenAt.Time.Equal(now) {
		t.Fatalf("status %+v", status)
	}
	dir := filepath.Join(deps.Root, "etc", "bedrock", "authn")
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", info, err)
	}
	for name, want := range map[string][]byte{"authentication.yaml": authentication, "webhook.kubeconfig": webhook} {
		path := filepath.Join(dir, name)
		if readFixtureFile(t, path) != string(want) {
			t.Fatalf("%s content", name)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", name, info, err)
		}
		if !ownedTempFile(owned, name) {
			t.Fatalf("%s must belong to the kube-apiserver user: %v", name, owned)
		}
	}
}

func ownedTempFile(owned []string, name string) bool {
	prefix := "." + name + "."
	for _, path := range owned {
		if strings.HasPrefix(filepath.Base(path), prefix) {
			return true
		}
	}
	return false
}

func TestSyncSkipsWorkers(t *testing.T) {
	putAuthnSources(t, "lab.example")
	deps := authnDeps(t, time.Now())
	if status := syncAuthnFiles(context.Background(), k8sClient, deps, hostWithRole("worker-a", v1alpha1.RoleWorkload)); status != nil {
		t.Fatalf("a worker reports no authn files: %+v", status)
	}
	if _, err := os.Stat(filepath.Join(deps.Root, "etc", "bedrock", "authn")); !os.IsNotExist(err) {
		t.Fatalf("a worker must not get the files: %v", err)
	}
}

func TestSyncKeepsFilesWhenSourceMissing(t *testing.T) {
	deps := authnDeps(t, time.Now())
	dir := filepath.Join(deps.Root, "etc", "bedrock", "authn")
	if _, err := apiserver.WriteFiles(dir, map[string][]byte{"authentication.yaml": []byte("old-a"), "webhook.kubeconfig": []byte("old-w")}, apiserver.KeepOwner); err != nil {
		t.Fatal(err)
	}
	written := metav1.NewTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	current := hostWithRole("cp-missing", v1alpha1.RoleControlPlane)
	current.Status.Authn = &v1alpha1.AuthnFilesStatus{Hash: "sha256:old", WrittenAt: &written}
	status := syncAuthnFiles(context.Background(), k8sClient, deps, current)
	if status == nil || status.Message != "waiting for bedrock-authn-apiserver" || status.Hash != "sha256:old" || !status.WrittenAt.Equal(&written) {
		t.Fatalf("status %+v", status)
	}
	if readFixtureFile(t, filepath.Join(dir, "authentication.yaml")) != "old-a" || readFixtureFile(t, filepath.Join(dir, "webhook.kubeconfig")) != "old-w" {
		t.Fatal("the files must stay while the source is missing")
	}
	if current.Status.Authn.Message != "" {
		t.Fatal("syncAuthnFiles must not change its input")
	}
}

func TestSyncWritesOnlyOnChange(t *testing.T) {
	putAuthnSources(t, "lab.example")
	first := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	deps := authnDeps(t, first)
	current := hostWithRole("cp-change", v1alpha1.RoleControlPlane)
	current.Status.Authn = syncAuthnFiles(context.Background(), k8sClient, deps, current)
	path := filepath.Join(deps.Root, "etc", "bedrock", "authn", "webhook.kubeconfig")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	deps.Now = func() time.Time { return first.Add(time.Hour) }
	again := syncAuthnFiles(context.Background(), k8sClient, deps, current)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.WrittenAt.Time.Equal(first) || !after.ModTime().Equal(before.ModTime()) || again.Hash != current.Status.Authn.Hash {
		t.Fatalf("unchanged sources must not rewrite: %+v", again)
	}
	putAuthnSources(t, "other.example")
	changed := syncAuthnFiles(context.Background(), k8sClient, deps, current)
	if !changed.WrittenAt.Time.Equal(first.Add(time.Hour)) || changed.Hash == current.Status.Authn.Hash {
		t.Fatalf("changed sources must be written: %+v", changed)
	}
	if !strings.Contains(readFixtureFile(t, path), "https://sso.other.example/webhook/v1/tokenreview") {
		t.Fatal("the webhook file must follow the source")
	}
}

func TestWebhookRestartPending(t *testing.T) {
	root := t.TempDir()
	start := time.Now().Add(-10 * time.Minute).Truncate(10 * time.Millisecond)
	fakeAPIServer(t, root, start)
	got, ok := apiserverStartTime(root)
	if !ok || !got.Equal(start) {
		t.Fatalf("start %v %v, want %v", got, ok, start)
	}
	if webhookRestartPending(root, start) {
		t.Fatal("no file, nothing pending")
	}
	path := filepath.Join(root, "etc", "bedrock", "authn", "webhook.kubeconfig")
	writeFixtureFile(t, path, "w")
	for _, tc := range []struct {
		modified time.Time
		pending  bool
	}{
		{start.Add(-time.Minute), false},
		{start.Add(time.Second), false},
		{start.Add(time.Minute), true},
	} {
		if err := os.Chtimes(path, tc.modified, tc.modified); err != nil {
			t.Fatal(err)
		}
		if got := webhookRestartPending(root, start); got != tc.pending {
			t.Fatalf("modified %v: pending %v, want %v", tc.modified.Sub(start), got, tc.pending)
		}
	}
	if _, ok := apiserverStartTime(t.TempDir()); ok {
		t.Fatal("no kube-apiserver process, no start time")
	}
}

func TestAgentReportsWebhookRestartPending(t *testing.T) {
	ctx := context.Background()
	node := "cp-restart"
	h := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: node}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	if err := k8sClient.Create(ctx, h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), h) })
	oldAuthentication, oldWebhook := putAuthnSources(t, "old.example")
	deps := authnDeps(t, time.Now())
	deps.Node = node
	fakeAPIServer(t, deps.Root, time.Now().Add(-time.Hour))
	apiserverCmdline(t, deps.Root, "--authentication-config=/etc/bedrock/authn/authentication.yaml")
	webhookPath := filepath.Join(deps.Root, "etc", "bedrock", "authn", "webhook.kubeconfig")
	if err := Tick(ctx, k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(webhookPath, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	if err := Tick(ctx, k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	status := getHost(t, node).Status.Authn
	if status == nil || status.WebhookRestartPending || status.Hash != apiserver.Hash(oldAuthentication, oldWebhook) {
		t.Fatalf("files written before the kube-apiserver start are not pending: %+v", status)
	}
	authentication, webhook := putAuthnSources(t, "new.example")
	if err := Tick(ctx, k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	status = getHost(t, node).Status.Authn
	if status == nil || !status.WebhookRestartPending || status.Hash != apiserver.Hash(authentication, webhook) || status.Message != "" {
		t.Fatalf("a platform.host change must report a pending kube-apiserver restart: %+v", status)
	}
	if !strings.Contains(readFixtureFile(t, filepath.Join(deps.Root, "etc", "bedrock", "authn", "authentication.yaml")), "url: https://sso.new.example") {
		t.Fatal("authentication.yaml must follow the new host at once")
	}
	fakeAPIServer(t, deps.Root, time.Now().Add(time.Minute))
	if err := Tick(ctx, k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if status := getHost(t, node).Status.Authn; status.WebhookRestartPending {
		t.Fatalf("a restarted kube-apiserver has read the new webhook file: %+v", status)
	}
}

func TestAPIServerStartTimeIgnoresTenantAPIServers(t *testing.T) {
	root := t.TempDir()
	start := time.Now().Add(-10 * time.Minute).Truncate(10 * time.Millisecond)
	fakeTenantAPIServer(t, root, 1000, start.Add(-time.Hour))
	fakeTenantAPIServer(t, root, 3000, start.Add(time.Hour))
	writeFixtureFile(t, filepath.Join(root, "var", "lib", "k0s", "bin", "kube-apiserver"), "kube-apiserver")
	if _, ok := apiserverStartTime(root); ok {
		t.Fatal("tenant kube-apiservers are not the one k0s runs")
	}
	fakeAPIServer(t, root, start)
	got, ok := apiserverStartTime(root)
	if !ok || !got.Equal(start) {
		t.Fatalf("start %v %v, want %v", got, ok, start)
	}
	path := filepath.Join(root, "etc", "bedrock", "authn", "webhook.kubeconfig")
	writeFixtureFile(t, path, "w")
	for _, tc := range []struct {
		modified time.Time
		pending  bool
	}{
		{start.Add(-time.Minute), false},
		{start.Add(time.Minute), true},
	} {
		if err := os.Chtimes(path, tc.modified, tc.modified); err != nil {
			t.Fatal(err)
		}
		if got := authnRestartWanted(root, controllerService()); got != tc.pending {
			t.Fatalf("modified %v: pending %v, want %v", tc.modified.Sub(start), got, tc.pending)
		}
	}
}
