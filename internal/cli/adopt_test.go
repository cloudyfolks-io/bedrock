package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const (
	monKeySecret  = "AQBt3W5nAAAAABAAmonSecretThatMustNeverShowUp7A=="
	cephKeySecret = "AQDm9kR0Znq2LhAAadminSecretThatMustNeverShowUp0g=="
)

func rookKeyFileContent(fsid string) string {
	return "fsid: " + fsid + "\nmon-secret: " + monKeySecret + "\nceph-username: client.admin\nceph-secret: " + cephKeySecret + "\n"
}

func writeKeyFile(t *testing.T, mode os.FileMode, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ceph-keys.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertNoKeyText(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range []string{monKeySecret, cephKeySecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s contains key text: %q", where, text)
		}
	}
}

func TestReadRookKeysAcceptsOwnerOnlyModes(t *testing.T) {
	want := rookKeys{FSID: labelFSID, MonSecret: monKeySecret, CephUsername: "client.admin", CephSecret: cephKeySecret}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := writeKeyFile(t, mode, rookKeyFileContent(labelFSID))
		got, err := readRookKeys(path, labelFSID)
		if err != nil {
			t.Fatalf("mode %04o: %v", mode, err)
		}
		if got != want {
			t.Fatalf("mode %04o: the keys differ from the file", mode)
		}
	}
}

func TestReadRookKeysIgnoresExtraKeys(t *testing.T) {
	path := writeKeyFile(t, 0o600, rookKeyFileContent(labelFSID)+"admin-secret: "+cephKeySecret+"\n")
	if _, err := readRookKeys(path, labelFSID); err != nil {
		t.Fatal(err)
	}
}

func TestReadRookKeysRefusals(t *testing.T) {
	valid := rookKeyFileContent(labelFSID)
	without := func(key string) string {
		var kept []string
		for _, line := range strings.Split(valid, "\n") {
			if !strings.HasPrefix(line, key+":") {
				kept = append(kept, line)
			}
		}
		return strings.Join(kept, "\n")
	}
	const notMapping = "not a YAML mapping of strings"
	cases := []struct {
		name    string
		mode    os.FileMode
		content string
		reason  string
	}{
		{"group readable", 0o640, valid, "mode is 0640, want 0600 or 0400"},
		{"world readable", 0o644, valid, "mode is 0644, want 0600 or 0400"},
		{"owner executable", 0o700, valid, "mode is 0700, want 0600 or 0400"},
		{"no access", 0o000, valid, "mode is 0000, want 0600 or 0400"},
		{"empty file", 0o600, "", "missing fsid"},
		{"broken yaml", 0o600, "fsid: [" + cephKeySecret + "\n", notMapping},
		{"broken yaml with tab", 0o600, "mon-secret:\t" + monKeySecret + ": " + cephKeySecret + "\n", notMapping},
		{"a list", 0o600, "- " + cephKeySecret + "\n- " + monKeySecret + "\n", notMapping},
		{"a bare scalar", 0o600, cephKeySecret + "\n", notMapping},
		{"nested value", 0o600, "mon-secret:\n  inner: " + monKeySecret + "\nceph-secret: " + cephKeySecret + "\n", notMapping},
		{"no fsid", 0o600, without("fsid"), "missing fsid"},
		{"no mon-secret", 0o600, without("mon-secret"), "missing mon-secret"},
		{"no ceph-username", 0o600, without("ceph-username"), "missing ceph-username"},
		{"no ceph-secret", 0o600, without("ceph-secret"), "missing ceph-secret"},
		{"empty mon-secret", 0o600, strings.Replace(valid, "mon-secret: "+monKeySecret, `mon-secret: ""`, 1), "missing mon-secret"},
		{"other fsid", 0o600, rookKeyFileContent(otherFSID), "fsid differs from storage.adopt.fsid"},
	}
	for _, tc := range cases {
		path := writeKeyFile(t, tc.mode, tc.content)
		_, err := readRookKeys(path, labelFSID)
		want := "adopt key file " + path + ": " + tc.reason
		if err == nil || err.Error() != want {
			t.Fatalf("%s: got %v, want %q", tc.name, err, want)
		}
		assertNoKeyText(t, tc.name, err.Error())
	}
}

func TestReadRookKeysFileProblems(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.yaml")
	cases := map[string]struct{ path, reason string }{
		"missing file": {missing, "no such file or directory"},
		"directory":    {dir, "not a regular file"},
	}
	for name, tc := range cases {
		_, err := readRookKeys(tc.path, labelFSID)
		want := "adopt key file " + tc.path + ": " + tc.reason
		if err == nil || err.Error() != want {
			t.Fatalf("%s: got %v, want %q", name, err, want)
		}
	}
}

func rookTestMonitors(ids ...string) []v1alpha1.AdoptMonitor {
	monitors := make([]v1alpha1.AdoptMonitor, 0, len(ids))
	for i, id := range ids {
		monitors = append(monitors, v1alpha1.AdoptMonitor{ID: id, Node: "node-" + string(rune('1'+i)), Address: "10.0.10." + string(rune('1'+i)) + "1"})
	}
	return monitors
}

func TestRookMonSecret(t *testing.T) {
	keys := rookKeys{FSID: labelFSID, MonSecret: monKeySecret, CephUsername: "client.admin", CephSecret: cephKeySecret}
	want := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon", Finalizers: []string{"ceph.rook.io/disaster-protection"}},
		Type:       "kubernetes.io/rook",
		Data: map[string][]byte{
			"fsid":          []byte(labelFSID),
			"mon-secret":    []byte(monKeySecret),
			"ceph-username": []byte("client.admin"),
			"ceph-secret":   []byte(cephKeySecret),
		},
	}
	if got := rookMonSecret(keys); !reflect.DeepEqual(got, want) {
		t.Fatalf("secret differs: %+v", got.ObjectMeta)
	}
}

func mustEndpoints(t *testing.T, monitors []v1alpha1.AdoptMonitor) *corev1.ConfigMap {
	t.Helper()
	endpoints, err := rookMonEndpoints(monitors)
	if err != nil {
		t.Fatal(err)
	}
	return endpoints
}

func TestRookMonEndpoints(t *testing.T) {
	got := mustEndpoints(t, rookTestMonitors("a", "b", "c"))
	want := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon-endpoints", Finalizers: []string{"ceph.rook.io/disaster-protection"}},
		Data: map[string]string{
			"data":     "a=10.0.10.11:6789,b=10.0.10.21:6789,c=10.0.10.31:6789",
			"mapping":  `{"node":{"a":{"Name":"node-1","Hostname":"node-1","Address":"10.0.10.11"},"b":{"Name":"node-2","Hostname":"node-2","Address":"10.0.10.21"},"c":{"Name":"node-3","Hostname":"node-3","Address":"10.0.10.31"}}}`,
			"maxMonId": "2",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configmap differs:\n%+v\nwant\n%+v", got, want)
	}
}

func TestRookMonEndpointsMaxMonID(t *testing.T) {
	cases := []struct {
		ids  []string
		want string
	}{
		{[]string{"a"}, "0"},
		{[]string{"c", "a", "b"}, "2"},
		{[]string{"a", "c", "o"}, "14"},
		{[]string{"a", "z"}, "25"},
		{[]string{"a", "aa"}, "26"},
		{[]string{"ab", "b"}, "27"},
	}
	for _, tc := range cases {
		if got := mustEndpoints(t, rookTestMonitors(tc.ids...)).Data["maxMonId"]; got != tc.want {
			t.Fatalf("ids %v: maxMonId %q, want %q", tc.ids, got, tc.want)
		}
	}
}

func rookTestSecret() *corev1.Secret {
	return rookMonSecret(rookKeys{FSID: labelFSID, MonSecret: monKeySecret, CephUsername: "client.admin", CephSecret: cephKeySecret})
}

func rookTestAdopt(keyFile string) *v1alpha1.AdoptSpec {
	return &v1alpha1.AdoptSpec{FSID: labelFSID, KeyFile: keyFile, Monitors: rookTestMonitors("a", "b", "c")}
}

func missingKeyFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "none.yaml")
}

func rookTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func rookObjectExists(c client.Client, obj client.Object, name string) bool {
	return c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: name}, obj) == nil
}

func getRookObjects(t *testing.T, c client.Client) (corev1.Secret, corev1.ConfigMap) {
	t.Helper()
	var secret corev1.Secret
	var endpoints corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, &secret); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon-endpoints"}, &endpoints); err != nil {
		t.Fatal(err)
	}
	return secret, endpoints
}

func assertNoRookObjects(t *testing.T, c client.Client) {
	t.Helper()
	if rookObjectExists(c, &corev1.Secret{}, "rook-ceph-mon") || rookObjectExists(c, &corev1.ConfigMap{}, "rook-ceph-mon-endpoints") {
		t.Fatal("a Rook identity object was written")
	}
}

func TestRookIdentityHookActsOnlyAfterTheRookGroup(t *testing.T) {
	c := rookTestClient(t)
	adopt := rookTestAdopt(missingKeyFile(t))
	hook := rookIdentityHook(c, adopt, rookTestSecret())
	if err := hook(context.Background(), release.Group{Name: "traefik"}); err != nil {
		t.Fatal(err)
	}
	assertNoRookObjects(t, c)
	if err := hook(context.Background(), release.Group{Name: "rook"}); err != nil {
		t.Fatal(err)
	}
	secret, endpoints := getRookObjects(t, c)
	want := rookTestSecret()
	if !reflect.DeepEqual(secret.Data, want.Data) || secret.Type != "kubernetes.io/rook" || !reflect.DeepEqual(secret.Finalizers, want.Finalizers) || len(secret.OwnerReferences) != 0 {
		t.Fatalf("secret %+v", secret.ObjectMeta)
	}
	wantEndpoints := mustEndpoints(t, adopt.Monitors)
	if !reflect.DeepEqual(endpoints.Data, wantEndpoints.Data) || !reflect.DeepEqual(endpoints.Finalizers, wantEndpoints.Finalizers) || len(endpoints.OwnerReferences) != 0 {
		t.Fatalf("configmap %+v", endpoints.ObjectMeta)
	}
}

func TestRookIdentityHookWithoutAdoptDoesNothing(t *testing.T) {
	c := rookTestClient(t)
	if err := rookIdentityHook(c, nil, nil)(context.Background(), release.Group{Name: "rook"}); err != nil {
		t.Fatal(err)
	}
	assertNoRookObjects(t, c)
}

func TestRookIdentityHookKeepsWhatRookChanged(t *testing.T) {
	c := rookTestClient(t)
	hook := rookIdentityHook(c, rookTestAdopt(missingKeyFile(t)), rookTestSecret())
	ctx := context.Background()
	if err := hook(ctx, release.Group{Name: "rook"}); err != nil {
		t.Fatal(err)
	}
	secret, endpoints := getRookObjects(t, c)
	secret.Data["ceph-secret"] = []byte("rotated-by-rook")
	endpoints.Data["maxMonId"] = "3"
	if err := c.Update(ctx, &secret); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(ctx, &endpoints); err != nil {
		t.Fatal(err)
	}
	if err := hook(ctx, release.Group{Name: "rook"}); err != nil {
		t.Fatal(err)
	}
	secret, endpoints = getRookObjects(t, c)
	if string(secret.Data["ceph-secret"]) != "rotated-by-rook" || endpoints.Data["maxMonId"] != "3" {
		t.Fatal("a second run overwrote the objects")
	}
}

func TestRookIdentityHookRefusesASecretOfAnotherCluster(t *testing.T) {
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, Data: map[string][]byte{"fsid": []byte(otherFSID)}}
	c := rookTestClient(t, existing)
	err := rookIdentityHook(c, rookTestAdopt(missingKeyFile(t)), rookTestSecret())(context.Background(), release.Group{Name: "rook"})
	if err == nil || err.Error() != "secret rook-ceph/rook-ceph-mon holds another fsid than storage.adopt.fsid" {
		t.Fatalf("error %v", err)
	}
	assertNoKeyText(t, "hook error", err.Error())
	if rookObjectExists(c, &corev1.ConfigMap{}, "rook-ceph-mon-endpoints") {
		t.Fatal("the configmap was written after the refusal")
	}
}

func TestRookIdentityHookNeedsNoKeyFileWhenTheSecretExists(t *testing.T) {
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, Data: map[string][]byte{"fsid": []byte(labelFSID), "ceph-secret": []byte("kept-by-rook")}}
	c := rookTestClient(t, existing)
	if err := rookIdentityHook(c, rookTestAdopt(missingKeyFile(t)), nil)(context.Background(), release.Group{Name: "rook"}); err != nil {
		t.Fatal(err)
	}
	secret, endpoints := getRookObjects(t, c)
	if string(secret.Data["ceph-secret"]) != "kept-by-rook" || len(secret.Data) != 2 {
		t.Fatal("the hook changed the secret")
	}
	if want := mustEndpoints(t, rookTestAdopt("").Monitors); !reflect.DeepEqual(endpoints.Data, want.Data) {
		t.Fatalf("configmap %v", endpoints.Data)
	}
}

func TestRookIdentityHookReadsTheKeyFileWhenTheSecretIsAbsent(t *testing.T) {
	c := rookTestClient(t)
	path := writeKeyFile(t, 0o600, rookKeyFileContent(labelFSID))
	if err := rookIdentityHook(c, rookTestAdopt(path), nil)(context.Background(), release.Group{Name: "rook"}); err != nil {
		t.Fatal(err)
	}
	secret, _ := getRookObjects(t, c)
	if !reflect.DeepEqual(secret.Data, rookTestSecret().Data) {
		t.Fatal("the secret differs from the key file")
	}
}

func TestRookIdentityHookStopsWithoutSecretAndKeyFile(t *testing.T) {
	c := rookTestClient(t)
	path := missingKeyFile(t)
	err := rookIdentityHook(c, rookTestAdopt(path), nil)(context.Background(), release.Group{Name: "rook"})
	if want := "adopt key file " + path + ": no such file or directory"; err == nil || err.Error() != want {
		t.Fatalf("error %v, want %q", err, want)
	}
	assertNoRookObjects(t, c)
}

func TestLoadRookMonSecretReadsTheKeyFile(t *testing.T) {
	path := writeKeyFile(t, 0o600, rookKeyFileContent(labelFSID))
	got, err := loadRookMonSecret(*rookTestAdopt(path))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rookTestSecret()) {
		t.Fatal("the secret differs from the key file")
	}
	missing := missingKeyFile(t)
	if _, err := loadRookMonSecret(*rookTestAdopt(missing)); err == nil || !strings.HasPrefix(err.Error(), "adopt key file "+missing+": ") {
		t.Fatalf("error %v", err)
	}
}

func TestRunInitAdoptStopsBeforeAnyHostChangeOnKeyFileProblem(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, keyPath string)
		reason string
	}{
		{"missing", func(t *testing.T, keyPath string) { mustRemove(t, keyPath) }, "no such file or directory"},
		{"readable by others", func(t *testing.T, keyPath string) { mustChmod(t, keyPath, 0o644) }, "mode is 0644, want 0600 or 0400"},
		{"malformed", func(t *testing.T, keyPath string) { mustWrite(t, keyPath, "fsid: ["+monKeySecret+"\n") }, "not a YAML mapping of strings"},
		{"no key", func(t *testing.T, keyPath string) {
			mustWrite(t, keyPath, "fsid: "+labelFSID+"\nmon-secret: "+monKeySecret+"\nceph-username: client.admin\n")
		}, "missing ceph-secret"},
		{"other cluster", func(t *testing.T, keyPath string) { mustWrite(t, keyPath, rookKeyFileContent(otherFSID)) }, "fsid differs from storage.adopt.fsid"},
	}
	for _, tc := range cases {
		root, e := adoptedHost(t)
		configPath := adoptConfigFile(t, root, adoptInitConfig)
		keyPath := filepath.Join(root, "ceph-keys.yaml")
		tc.mutate(t, keyPath)
		var out, errOut bytes.Buffer
		code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--data-dir", filepath.Join(root, "var", "lib", "k0s")}, adoptDeps(root, e), &out, &errOut)
		if code != 1 {
			t.Fatalf("%s: exit %d\nstdout %s\nstderr %s", tc.name, code, out.String(), errOut.String())
		}
		if want := "adopt key file " + keyPath + ": " + tc.reason + "\n"; errOut.String() != want {
			t.Fatalf("%s: stderr %q, want %q", tc.name, errOut.String(), want)
		}
		assertNoKeyText(t, tc.name+" stdout", out.String())
		assertNoKeyText(t, tc.name+" stderr", errOut.String())
		statusCall := k0s.DefaultBinary + " status --data-dir " + filepath.Join(root, "var", "lib", "k0s")
		for _, call := range e.Calls {
			if call != statusCall {
				t.Fatalf("%s: init ran a host command before it read the key file: %s", tc.name, call)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "k0s.yaml")); err == nil {
			t.Fatalf("%s: init wrote k0s.yaml", tc.name)
		}
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func mustChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseWithRookGroup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "release", "testdata", "good"))); err != nil {
		t.Fatal(err)
	}
	group := filepath.Join(dir, "manifests", "40-rook")
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(group, "namespace.yaml"), []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: rook-ceph\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type adoptInitRun struct {
	c       client.Client
	keyPath string
	code    int
	out     string
	errOut  string
}

func runAdoptInit(t *testing.T, running bool, prepare func(t *testing.T, c client.Client, keyPath string)) adoptInitRun {
	t.Helper()
	c, newClient := startEnv(t)
	root, e := adoptedHost(t)
	configPath := adoptConfigFile(t, root, adoptInitConfig)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, name := range []string{"kube-system", "bedrock-system"} {
		if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})); err != nil {
			t.Fatal(err)
		}
	}
	master := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, master); err != nil {
		t.Fatal(err)
	}
	master.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.10.11"}}
	if err := c.Status().Update(ctx, master); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ctx.Err() == nil {
			var cluster v1alpha1.Cluster
			if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err == nil && cluster.Status.Version == "" {
				cluster.Status.Version = "v0.1.0-test"
				_ = c.Status().Update(ctx, &cluster)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	statusCall := "/usr/local/bin/k0s status --data-dir " + dataDir
	if running {
		e.Responses[statusCall] = ""
	} else {
		installArgs := k0s.InstallArgs(k0s.InstallOptions{
			Role: "controller", Force: true, ConfigPath: filepath.Join(root, "etc", "k0s", "k0s.yaml"), EnableWorker: true, NoTaints: true, DynamicConfig: true,
			Labels:            roles.Labels([]string{"control-plane", "ceph-osd", "fabric-gateway"}),
			KubeletExtraArgs:  []string{"--node-status-update-frequency=4s"},
			DataDir:           dataDir,
			KubeletRootDir:    k0s.DefaultKubeletRootDir,
			DisableComponents: k0s.DefaultDisabledComponents,
		})
		e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
		e.Errors[statusCall] = &host.ExitError{Code: 1}
	}
	keyPath := filepath.Join(root, "ceph-keys.yaml")
	if prepare != nil {
		prepare(t, c, keyPath)
	}
	deps := adoptDeps(root, e)
	deps.NewClient = newClient
	deps.Executable = fakeExecutable(t)
	var out, errOut bytes.Buffer
	code := RunInit(ctx, []string{"-f", configPath, "--release-dir", releaseWithRookGroup(t), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir, "--timeout", "30s"}, deps, &out, &errOut)
	assertNoKeyText(t, "stdout", out.String())
	assertNoKeyText(t, "stderr", errOut.String())
	return adoptInitRun{c: c, keyPath: keyPath, code: code, out: out.String(), errOut: errOut.String()}
}

func createRookSecret(t *testing.T, c client.Client, fsid, cephSecret string) {
	t.Helper()
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph"}})); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, Data: map[string][]byte{"fsid": []byte(fsid), "ceph-secret": []byte(cephSecret)}}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
}

func assertRookSecretHoldsKeyFile(t *testing.T, c client.Client) {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["fsid"]) != labelFSID || string(secret.Data["mon-secret"]) != monKeySecret || string(secret.Data["ceph-secret"]) != cephKeySecret || string(secret.Data["ceph-username"]) != "client.admin" {
		t.Fatal("the secret does not hold the key file values")
	}
}

func TestRunInitAdoptWritesTheIdentityTheClusterAndTheDevices(t *testing.T) {
	run := runAdoptInit(t, false, nil)
	if run.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", run.code, run.out, run.errOut)
	}
	ctx := context.Background()
	assertRookSecretHoldsKeyFile(t, run.c)
	var endpoints corev1.ConfigMap
	if err := run.c.Get(ctx, client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon-endpoints"}, &endpoints); err != nil {
		t.Fatal(err)
	}
	if endpoints.Data["data"] != "a=10.0.10.11:6789" || endpoints.Data["maxMonId"] != "0" {
		t.Fatalf("endpoints %v", endpoints.Data)
	}
	var cluster v1alpha1.Cluster
	if err := run.c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	adopt := cluster.Spec.Storage.Adopt
	if adopt == nil || adopt.FSID != labelFSID || adopt.KeyFile != "" || len(adopt.Hosts) != 2 || adopt.Pools.Block != "storage-block" {
		t.Fatalf("cluster adopt %+v", adopt)
	}
	var h v1alpha1.Host
	if err := run.c.Get(ctx, client.ObjectKey{Name: "node-1"}, &h); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.Spec.Storage.Devices, []string{osdDisk}) {
		t.Fatalf("devices %v", h.Spec.Storage.Devices)
	}
}

func TestRunInitAdoptRerunNeedsNoKeyFileWhenTheSecretExists(t *testing.T) {
	run := runAdoptInit(t, true, func(t *testing.T, c client.Client, keyPath string) {
		mustRemove(t, keyPath)
		createRookSecret(t, c, labelFSID, "kept-by-rook")
	})
	if run.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", run.code, run.out, run.errOut)
	}
	var secret corev1.Secret
	if err := run.c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["ceph-secret"]) != "kept-by-rook" || len(secret.Data) != 2 {
		t.Fatal("the re-run changed the secret")
	}
	var endpoints corev1.ConfigMap
	if err := run.c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon-endpoints"}, &endpoints); err != nil {
		t.Fatal(err)
	}
}

func TestRunInitAdoptRerunNeedsTheKeyFileWhenTheSecretIsAbsent(t *testing.T) {
	run := runAdoptInit(t, true, func(t *testing.T, c client.Client, keyPath string) { mustRemove(t, keyPath) })
	if run.code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", run.code, run.out, run.errOut)
	}
	if want := "adopt key file " + run.keyPath + ": no such file or directory"; !strings.Contains(run.errOut, want) {
		t.Fatalf("stderr %q lacks %q", run.errOut, want)
	}
	if !strings.Contains(run.out, "group rook failed") {
		t.Fatalf("a re-run must read the key file at the rook group, stdout %s", run.out)
	}
	var secret corev1.Secret
	if err := run.c.Get(context.Background(), client.ObjectKey{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, &secret); err == nil {
		t.Fatal("a secret was written without a key file")
	}
}

func TestRunInitAdoptRerunWritesTheIdentityWhenTheSecretIsAbsent(t *testing.T) {
	run := runAdoptInit(t, true, nil)
	if run.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", run.code, run.out, run.errOut)
	}
	assertRookSecretHoldsKeyFile(t, run.c)
}
