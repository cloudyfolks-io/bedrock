package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/audit"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const initConfig = `apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterConfig
metadata:
  name: lab
spec:
  version: v0.1.0-test
  api:
    vip: 10.0.10.10
  platform:
    host: cloud.example.com
  network:
    managementInterface: bond0.10
  storage:
    devices: [/dev/sdb]
  roles: [control-plane, ceph-osd, fabric-gateway]
`

const initConfigWithMirror = `apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterConfig
metadata:
  name: lab
spec:
  version: v0.1.0-test
  api:
    vip: 10.0.10.10
  platform:
    host: cloud.example.com
  network:
    managementInterface: bond0.10
  storage:
    devices: [/dev/sdb]
  registry:
    mirror: https://mirror.example.com
  roles: [control-plane, ceph-osd, fabric-gateway]
`

type devInfo struct{ mode fs.FileMode }

func (d devInfo) Name() string       { return "sdb" }
func (d devInfo) Size() int64        { return 0 }
func (d devInfo) Mode() fs.FileMode  { return d.mode }
func (d devInfo) ModTime() time.Time { return time.Time{} }
func (d devInfo) IsDir() bool        { return false }
func (d devInfo) Sys() any           { return nil }

func startEnv(t *testing.T) (client.Client, func(string) (client.Client, error)) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "manifests", "00-crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c, func(string) (client.Client, error) { return c, nil }
}

func fakeHost(t *testing.T) (string, *host.FakeExec) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"etc", "sys/fs/cgroup", "dev", "run/systemd/system", "var/lib"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys", "fs", "cgroup", "cgroup.controllers"), []byte("cpu\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "dev", "kvm"), nil, 0o600)
	e := &host.FakeExec{Responses: map[string]string{
		"ip -json route show default":                 `[{"dst":"default","dev":"bond0.10"}]`,
		"ip -json -4 addr show dev bond0.10":          `[{"addr_info":[{"family":"inet","local":"10.0.10.11"}]}]`,
		"ip -json link show":                          `[{"ifname":"lo"},{"ifname":"bond0.10"}]`,
		"timedatectl show -p NTPSynchronized --value": "yes\n",
		"hostname": "node-1\n",
		"ip addr replace 10.0.10.10/32 dev bond0.10":   "",
		"systemctl daemon-reload":                      "",
		"systemctl enable bedrock-vip.service":         "",
		"systemctl enable --now bedrock-agent.service": "",
		"ip -json addr":                                `[{"addr_info":[{"family":"inet","local":"10.0.10.11"}]}]`,
		"/usr/local/bin/k0s version":                   "v1.36.3+k0s.0\n",
		"/usr/local/bin/k0s start":                     "",
		"/usr/local/bin/k0s kubectl get --raw=/readyz": "ok",
	}, Errors: map[string]error{
		"blkid -p -o value -s TYPE /dev/sdb": &host.ExitError{Code: 2},
		"ping -c 1 -W 1 10.0.10.10":          &host.ExitError{Code: 1},
	}}
	return root, e
}

func fakeExecutable(t *testing.T) func() (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bedrock")
	if err := os.WriteFile(path, []byte("bedrock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() (string, error) { return path, nil }
}

func assertAgentInstalled(t *testing.T, root, dataDir string, executable func() (string, error)) {
	t.Helper()
	unit, err := os.ReadFile(filepath.Join(root, "etc", "systemd", "system", "bedrock-agent.service"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unit) != host.AgentUnit(host.DefaultAgentBinary, filepath.Join(dataDir, "kubelet.conf")) {
		t.Fatalf("agent unit %q", unit)
	}
	self, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, host.DefaultAgentBinary))
	if err != nil || string(got) != string(want) {
		t.Fatalf("binary %q %v", got, err)
	}
}

func TestRunInitHappyPath(t *testing.T) {
	c, newClient := startEnv(t)
	root, e := fakeHost(t)
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, []byte(initConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})); err != nil {
		t.Fatal(err)
	}
	_ = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}})
	master := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, master); err != nil {
		t.Fatal(err)
	}
	master.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.10.11"}}
	if err := c.Status().Update(ctx, master); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
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
	k0sConfigPath := filepath.Join(root, "etc", "k0s", "k0s.yaml")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: k0sConfigPath, EnableWorker: true, NoTaints: true, DynamicConfig: true,
		Labels:            roles.Labels([]string{"control-plane", "ceph-osd", "fabric-gateway"}),
		KubeletExtraArgs:  []string{"--node-status-update-frequency=4s"},
		DataDir:           dataDir,
		KubeletRootDir:    k0s.DefaultKubeletRootDir,
		DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	executable := fakeExecutable(t)
	deps := InitDeps{
		Exec:       e,
		Uid:        0,
		FreeBytes:  func(string) (uint64, error) { return 100 << 30, nil },
		Stat:       func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil },
		Root:       root,
		NewClient:  newClient,
		Executable: executable,
	}
	var out, errOut bytes.Buffer
	code := RunInit(ctx, []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir, "--timeout", "30s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "k0s.yaml")); err != nil {
		t.Fatal("k0s.yaml not written")
	}
	vipUnit, err := os.ReadFile(filepath.Join(root, "etc", "systemd", "system", "bedrock-vip.service"))
	if err != nil || string(vipUnit) != host.VIPUnit("10.0.10.10", "bond0.10") {
		t.Fatalf("vip unit %q %v", vipUnit, err)
	}
	vipEnabled := false
	for _, call := range e.Calls {
		if call == "systemctl enable bedrock-vip.service" {
			vipEnabled = true
		}
	}
	if !vipEnabled {
		t.Fatal("expected the vip unit to be enabled")
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kube-vip"}, &cm); err != nil || cm.Data["address"] != "10.0.10.10" || cm.Data["vip_interface"] != "bond0.10" {
		t.Fatalf("kube-vip configmap %v %v", cm.Data, err)
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil || cluster.Spec.DesiredVersion != "v0.1.0-test" {
		t.Fatalf("cluster %v %v", cluster.Spec, err)
	}
	var h v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "node-1"}, &h); err != nil || len(h.Spec.Roles) != 3 {
		t.Fatalf("host %v %v", h.Spec, err)
	}
	var setting v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: "storage.replicas"}, &setting); err != nil || setting.Spec.Value != "1" {
		t.Fatalf("setting %v %v", setting.Spec, err)
	}
	joined := ""
	for _, call := range e.Calls {
		if len(call) > 30 && call[:30] == "/usr/local/bin/k0s install con" {
			joined = call
		}
	}
	for _, want := range []string{"--force", "--enable-worker", "--no-taints", "--enable-dynamic-config", "bedrock.cloudyfolks.io/role-control-plane=true", "fabric/role=master", "--disable-components konnectivity-server,metrics-server,helm"} {
		if !bytes.Contains([]byte(joined), []byte(want)) {
			t.Fatalf("install call %q lacks %q", joined, want)
		}
	}
	if !bytes.Contains(out.Bytes(), []byte("cluster v0.1.0-test ready")) {
		t.Fatalf("stdout %s", out.String())
	}
	assertAgentInstalled(t, root, dataDir, executable)
	assertDeviceOwnership(t, root)
}

func assertDeviceOwnership(t *testing.T, root string) {
	t.Helper()
	dropIn, err := os.ReadFile(filepath.Join(root, "etc", "k0s", "containerd.d", "cri-devices.toml"))
	if err != nil || !strings.Contains(string(dropIn), "device_ownership_from_security_context = true") {
		t.Fatalf("containerd device ownership drop-in %q %v", dropIn, err)
	}
}

func TestRunInitSkipsInstallWhenAlreadyRunning(t *testing.T) {
	c, newClient := startEnv(t)
	root, e := fakeHost(t)
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, []byte(initConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})); err != nil {
		t.Fatal(err)
	}
	_ = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}})
	master := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, master); err != nil {
		t.Fatal(err)
	}
	master.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.10.11"}}
	if err := c.Status().Update(ctx, master); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
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
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	executable := fakeExecutable(t)
	deps := InitDeps{
		Exec:       e,
		Uid:        0,
		FreeBytes:  func(string) (uint64, error) { return 100 << 30, nil },
		Stat:       func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil },
		Root:       root,
		NewClient:  newClient,
		Executable: executable,
	}
	var out, errOut bytes.Buffer
	code := RunInit(ctx, []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir, "--timeout", "30s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	for _, call := range e.Calls {
		if strings.Contains(call, "k0s install") || call == "/usr/local/bin/k0s start" {
			t.Fatalf("k0s must not be reinstalled or restarted when already running, got %q", call)
		}
	}
}

func TestLoadBundleRemovesExtractedDirAfterCleanup(t *testing.T) {
	var extracted string
	deps := InitDeps{FromImage: func(ctx context.Context, ref, arch, dest string) error {
		extracted = dest
		if err := os.MkdirAll(filepath.Join(dest, "manifests", "00-empty"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, "release.yaml"), []byte("version: v0.1.0-test\nk0sVersion: v1.36.3+k0s.0\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, "images.txt"), nil, 0o644)
	}}
	bundle, cleanup, err := loadBundle(context.Background(), initOptions{image: "ref"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Spec.Version != "v0.1.0-test" {
		t.Fatalf("version %s", bundle.Spec.Version)
	}
	if _, err := os.Stat(extracted); err != nil {
		t.Fatal("extracted dir must exist before cleanup")
	}
	cleanup()
	if _, err := os.Stat(extracted); !os.IsNotExist(err) {
		t.Fatal("extracted release dir must be removed after cleanup")
	}
}

func TestLoadBundleKeepsProvidedReleaseDir(t *testing.T) {
	dir := filepath.Join("..", "release", "testdata", "good")
	_, cleanup, err := loadBundle(context.Background(), initOptions{releaseDir: dir}, InitDeps{})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("a provided release dir must not be removed")
	}
}

func TestRunInitRejectsConfigVersionMismatch(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "cluster.yaml")
	mismatched := strings.Replace(initConfig, "version: v0.1.0-test", "version: v9.9.9", 1)
	if err := os.WriteFile(configPath, []byte(mismatched), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good")}, InitDeps{}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	want := "config version v9.9.9 does not match release v0.1.0-test"
	if !strings.Contains(errOut.String(), want) {
		t.Fatalf("stderr %q, want to contain %q", errOut.String(), want)
	}
}

func TestRunInitBlockedByPreflight(t *testing.T) {
	_, newClient := startEnv(t)
	root, e := fakeHost(t)
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, []byte(initConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := InitDeps{Exec: e, Uid: 1000, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Stat: func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil }, Root: root, NewClient: newClient}
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--data-dir", filepath.Join(root, "var", "lib", "k0s")}, deps, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("[fail] root")) {
		t.Fatalf("stdout %s", out.String())
	}
	for _, call := range e.Calls {
		if bytes.Contains([]byte(call), []byte("k0s install")) {
			t.Fatal("k0s must not be installed when preflight blocks")
		}
	}
}

const fakeK0sContent = "fake-k0s-binary-content"

var fixtureArch = goruntime.GOARCH

func buildFixtureBundle(t *testing.T, version, k0sVersion string) (path, checksum string) {
	t.Helper()
	releaseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(releaseDir, "manifests", "00-crds"), 0o755); err != nil {
		t.Fatal(err)
	}
	k0sSrc := filepath.Join(t.TempDir(), "k0s-src")
	if err := os.WriteFile(k0sSrc, []byte(fakeK0sContent), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := release.FileSHA256(k0sSrc)
	if err != nil {
		t.Fatal(err)
	}
	bedrockSrc := filepath.Join(t.TempDir(), "bedrock-src")
	if err := os.WriteFile(bedrockSrc, []byte("bedrock"), 0o755); err != nil {
		t.Fatal(err)
	}
	bedrockSum, err := release.FileSHA256(bedrockSrc)
	if err != nil {
		t.Fatal(err)
	}
	releaseYAML := fmt.Sprintf("version: %s\nimage: ghcr.io/cloudyfolks-io/bedrock:%s\nk0sVersion: %s\nk0sChecksums:\n  %s: %s\nbedrockChecksums:\n  %s: %s\nsupportedOS:\n  - ubuntu-24.04\n", version, version, k0sVersion, fixtureArch, sum, fixtureArch, bedrockSum)
	if err := os.WriteFile(filepath.Join(releaseDir, "release.yaml"), []byte(releaseYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "images.txt"), []byte("quay.io/a/b@sha256:"+strings.Repeat("1", 64)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "manifests", "00-crds", "a.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  namespace: default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	if err := os.WriteFile(airgap, []byte("airgap"), 0o644); err != nil {
		t.Fatal(err)
	}
	pull := func(_ context.Context, ref, dest string) (string, error) {
		return "sha256:" + strings.Repeat("e", 64), os.WriteFile(dest, []byte("layout:"+ref), 0o644)
	}
	work := t.TempDir()
	if _, err := release.BuildBundle(context.Background(), release.BundleInputs{ReleaseDir: releaseDir, Arch: fixtureArch, K0sBinary: k0sSrc, BedrockBinary: bedrockSrc, K0sAirgap: airgap, Pull: pull}, work); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle.tar.zst")
	if err := release.PackBundle(work, out); err != nil {
		t.Fatal(err)
	}
	return out, sum
}

func TestRunInitFromBundle(t *testing.T) {
	c, newClient := startEnv(t)
	root, e := fakeHost(t)
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, []byte(initConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})); err != nil {
		t.Fatal(err)
	}
	_ = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}})
	master := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, master); err != nil {
		t.Fatal(err)
	}
	master.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.10.11"}}
	if err := c.Status().Update(ctx, master); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			var cluster v1alpha1.Cluster
			if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err == nil && cluster.Status.Version == "" {
				cluster.Status.Version = "v0.1.0-test"
				_ = c.Status().Update(ctx, &cluster)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	bundlePath, _ := buildFixtureBundle(t, "v0.1.0-test", "v1.99.0+k0s.0")
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	workDir := filepath.Join(root, "var", "lib", "bedrock")
	k0sBin := filepath.Join(root, "usr", "local", "bin", "k0s")
	k0sConfigPath := filepath.Join(root, "etc", "k0s", "k0s.yaml")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: k0sConfigPath, EnableWorker: true, NoTaints: true, DynamicConfig: true,
		Labels:            roles.Labels([]string{"control-plane", "ceph-osd", "fabric-gateway"}),
		KubeletExtraArgs:  []string{"--node-status-update-frequency=4s"},
		DataDir:           dataDir,
		KubeletRootDir:    k0s.DefaultKubeletRootDir,
		DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses[k0sBin+" "+strings.Join(installArgs, " ")] = ""
	e.Responses[k0sBin+" start"] = ""
	e.Responses[k0sBin+" kubectl get --raw=/readyz"] = "ok"
	e.Errors[k0sBin+" status --data-dir "+dataDir] = &host.ExitError{Code: 1}

	executable := fakeExecutable(t)
	deps := InitDeps{
		Exec:       e,
		Uid:        0,
		FreeBytes:  func(string) (uint64, error) { return 100 << 30, nil },
		Stat:       func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil },
		Root:       root,
		NewClient:  newClient,
		Executable: executable,
	}
	var out, errOut bytes.Buffer
	code := RunInit(ctx, []string{"-f", configPath, "--bundle", bundlePath, "--work-dir", workDir, "--k0s-bin", k0sBin, "--data-dir", dataDir, "--timeout", "30s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	got, err := os.ReadFile(k0sBin)
	if err != nil {
		t.Fatalf("k0s binary not installed from bundle: %v", err)
	}
	if string(got) != fakeK0sContent {
		t.Fatalf("k0s binary content %q, want %q", got, fakeK0sContent)
	}
	info, err := os.Stat(k0sBin)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("k0s binary mode %v %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	tars, airgap := 0, false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tar") {
			tars++
		}
		if entry.Name() == "k0s-airgap.tar" {
			airgap = true
		}
	}
	if tars != 3 || !airgap {
		t.Fatalf("images dir entries %v", entries)
	}
	if _, err := os.Stat(filepath.Join(workDir, "bundle")); !os.IsNotExist(err) {
		t.Fatal("extracted bundle dir must be removed after a successful init")
	}
}

func TestRunInitWritesMirror(t *testing.T) {
	c, newClient := startEnv(t)
	root, e := fakeHost(t)
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, []byte(initConfigWithMirror), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})); err != nil {
		t.Fatal(err)
	}
	_ = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}})
	master := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"fabric/role": "master"}}}
	if err := c.Create(ctx, master); err != nil {
		t.Fatal(err)
	}
	master.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.10.11"}}
	if err := c.Status().Update(ctx, master); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
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
	k0sConfigPath := filepath.Join(root, "etc", "k0s", "k0s.yaml")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: k0sConfigPath, EnableWorker: true, NoTaints: true, DynamicConfig: true,
		Labels:            roles.Labels([]string{"control-plane", "ceph-osd", "fabric-gateway"}),
		KubeletExtraArgs:  []string{"--node-status-update-frequency=4s"},
		DataDir:           dataDir,
		KubeletRootDir:    k0s.DefaultKubeletRootDir,
		DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	executable := fakeExecutable(t)
	deps := InitDeps{
		Exec:       e,
		Uid:        0,
		FreeBytes:  func(string) (uint64, error) { return 100 << 30, nil },
		Stat:       func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil },
		Root:       root,
		NewClient:  newClient,
		Executable: executable,
	}
	var out, errOut bytes.Buffer
	code := RunInit(ctx, []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir, "--timeout", "30s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	hosts, err := os.ReadFile(filepath.Join(root, "etc", "k0s", "containerd.d", "certs.d", "_default", "hosts.toml"))
	if err != nil {
		t.Fatalf("mirror hosts.toml not written: %v", err)
	}
	if !strings.Contains(string(hosts), `[host."https://mirror.example.com"]`) {
		t.Fatalf("hosts.toml %q", hosts)
	}
}

func TestKubeVIPDataEnablesServicesMode(t *testing.T) {
	data := kubeVIPData("10.0.0.250", "eth0")
	if data["svc_enable"] != "true" || data["cp_enable"] != "true" || data["address"] != "10.0.0.250" {
		t.Fatalf("data %+v", data)
	}
}

type initRun struct {
	c    client.Client
	root string
	deps InitDeps
	args []string
}

func prepareInit(t *testing.T) initRun {
	t.Helper()
	c, newClient := startEnv(t)
	root, e := fakeHost(t)
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, []byte(initConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})); err != nil {
		t.Fatal(err)
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
		for {
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
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: filepath.Join(root, "etc", "k0s", "k0s.yaml"), EnableWorker: true, NoTaints: true, DynamicConfig: true,
		Labels:            roles.Labels([]string{"control-plane", "ceph-osd", "fabric-gateway"}),
		KubeletExtraArgs:  []string{"--node-status-update-frequency=4s"},
		DataDir:           dataDir,
		KubeletRootDir:    k0s.DefaultKubeletRootDir,
		DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	deps := InitDeps{
		Exec:       e,
		Uid:        0,
		FreeBytes:  func(string) (uint64, error) { return 100 << 30, nil },
		Stat:       func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil },
		Root:       root,
		NewClient:  newClient,
		Executable: fakeExecutable(t),
	}
	args := []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir, "--timeout", "30s"}
	return initRun{c: c, root: root, deps: deps, args: args}
}

func runPreparedInit(t *testing.T, run initRun) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := RunInit(context.Background(), run.args, run.deps, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	return out.String()
}

func readInitFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestInitWritesAuthnFilesBeforeK0s(t *testing.T) {
	run := prepareInit(t)
	present := false
	run.deps.Exec = filesAtStart{FakeExec: run.deps.Exec.(*host.FakeExec), paths: authnPaths(run.root), present: &present}
	runPreparedInit(t, run)
	if !present {
		t.Fatal("the authn files must exist before k0s starts")
	}
	for _, path := range authnPaths(run.root) {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", path, info, err)
		}
	}
	ca := readInitFile(t, filepath.Join(run.root, "var", "lib", "bedrock", "authn", "ca.crt"))
	var doc map[string]any
	if err := sigyaml.Unmarshal([]byte(readInitFile(t, authnPaths(run.root)[0])), &doc); err != nil {
		t.Fatal(err)
	}
	issuer := doc["jwt"].([]any)[0].(map[string]any)["issuer"].(map[string]any)
	if issuer["url"] != "https://sso.cloud.example.com" || issuer["certificateAuthority"] != ca {
		t.Fatalf("issuer %v", issuer)
	}
}

func TestInitStartsWithAuditEnabled(t *testing.T) {
	run := prepareInit(t)
	present := false
	policy := filepath.Join(run.root, "etc", "bedrock", "audit", "policy.yaml")
	run.deps.Exec = filesAtStart{FakeExec: run.deps.Exec.(*host.FakeExec), paths: []string{policy, filepath.Join(run.root, "var", "log", "bedrock", "audit")}, present: &present}
	runPreparedInit(t, run)
	if !present {
		t.Fatal("the audit files must exist before k0s starts")
	}
	info, err := os.Stat(policy)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("policy %v %v", info, err)
	}
	want, err := audit.Policy("Metadata")
	if err != nil || readInitFile(t, policy) != string(want) {
		t.Fatalf("the policy must be the default level: %v", err)
	}
	if dir, err := os.Stat(filepath.Join(run.root, "var", "log", "bedrock", "audit")); err != nil || !dir.IsDir() {
		t.Fatalf("log directory %v %v", dir, err)
	}
	args := initExtraArgs(t, run)
	if len(args) < 8 {
		t.Fatalf("extraArgs %v", args)
	}
	for name, value := range audit.Args() {
		if args[name] != value {
			t.Errorf("%s = %v, want %s", name, args[name], value)
		}
	}
}

func TestInitKeepsAnEditedAuditPolicy(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	policy := filepath.Join(run.root, "etc", "bedrock", "audit", "policy.yaml")
	if err := os.WriteFile(policy, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPreparedInit(t, run)
	if readInitFile(t, policy) != "edited" {
		t.Fatal("a second init must not reset the audit policy")
	}
}

func TestInitKeepsTheAuthnArgs(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	args := initExtraArgs(t, run)
	for name, value := range apiserver.Args() {
		if args[name] != value {
			t.Errorf("%s = %v, want %s", name, args[name], value)
		}
	}
}

func initExtraArgs(t *testing.T, run initRun) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := sigyaml.Unmarshal([]byte(readInitFile(t, filepath.Join(run.root, "etc", "k0s", "k0s.yaml"))), &doc); err != nil {
		t.Fatal(err)
	}
	return doc["spec"].(map[string]any)["api"].(map[string]any)["extraArgs"].(map[string]any)
}

func TestInitKeepsExistingCA(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	bootstrap := filepath.Join(run.root, "var", "lib", "bedrock", "authn")
	firstKey := readInitFile(t, filepath.Join(bootstrap, "ca.key"))
	firstAuthentication := readInitFile(t, authnPaths(run.root)[0])
	firstWebhook := readInitFile(t, authnPaths(run.root)[1])
	if info, err := os.Stat(bootstrap); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("bootstrap dir %v %v", info, err)
	}
	runPreparedInit(t, run)
	if readInitFile(t, filepath.Join(bootstrap, "ca.key")) != firstKey || readInitFile(t, authnPaths(run.root)[0]) != firstAuthentication || readInitFile(t, authnPaths(run.root)[1]) != firstWebhook {
		t.Fatal("a second init must keep the CA and the bearer")
	}
	var ca corev1.Secret
	if err := run.c.Get(context.Background(), client.ObjectKey{Namespace: "cert-manager", Name: "bedrock-ca"}, &ca); err != nil {
		t.Fatal(err)
	}
	if string(ca.Data["tls.key"]) != firstKey {
		t.Fatal("the Secret must hold the kept CA key")
	}
	markRunning(run)
	runPreparedInit(t, run)
	if readInitFile(t, filepath.Join(bootstrap, "ca.key")) != firstKey || readInitFile(t, authnPaths(run.root)[1]) != firstWebhook {
		t.Fatal("a re-run on a running cluster must keep the CA and the bearer")
	}
}

func markRunning(run initRun) {
	e := run.deps.Exec.(*host.FakeExec)
	status := "/usr/local/bin/k0s status --data-dir " + filepath.Join(run.root, "var", "lib", "k0s")
	delete(e.Errors, status)
	e.Responses[status] = ""
}

func authnHostFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, path := range authnPaths(root) {
		files[path] = readInitFile(t, path)
	}
	return files
}

func TestInitRestoresLostBootstrapFromTheCluster(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	before := authnHostFiles(t, run.root)
	bootstrap := filepath.Join(run.root, "var", "lib", "bedrock", "authn")
	saved := bootstrapFilesIn(t, bootstrap)
	if err := os.RemoveAll(bootstrap); err != nil {
		t.Fatal(err)
	}
	markRunning(run)
	runPreparedInit(t, run)
	if !reflect.DeepEqual(bootstrapFilesIn(t, bootstrap), saved) {
		t.Fatal("the bootstrap dir must come back from the cluster Secrets")
	}
	if info, err := os.Stat(bootstrap); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("bootstrap dir %v %v", info, err)
	}
	if !reflect.DeepEqual(authnHostFiles(t, run.root), before) {
		t.Fatal("the authn host files must stay")
	}
}

func bootstrapFilesIn(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"ca.crt", "ca.key", "webhook-token"} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", name, info, err)
		}
		files[name] = readInitFile(t, path)
	}
	return files
}

func TestInitRefusesLostBootstrapWithPartialClusterMaterial(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	before := authnHostFiles(t, run.root)
	bootstrap := filepath.Join(run.root, "var", "lib", "bedrock", "authn")
	if err := os.RemoveAll(bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := run.c.Delete(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: "bedrock-authn-webhook-token"}}); err != nil {
		t.Fatal(err)
	}
	markRunning(run)
	var out, errOut bytes.Buffer
	if code := RunInit(context.Background(), run.args, run.deps, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "cert-manager/bedrock-ca") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if _, err := os.Stat(bootstrap); !os.IsNotExist(err) {
		t.Fatal("no new CA may be saved while the cluster holds the old one")
	}
	if !reflect.DeepEqual(authnHostFiles(t, run.root), before) {
		t.Fatal("the authn host files must stay")
	}
}

func TestInitOnAnUpgradedClusterTakesTheClusterMaterial(t *testing.T) {
	run := prepareInit(t)
	ctx := context.Background()
	certPEM, keyPEM, err := apiserver.NewCA(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bearer := "operator-made-bearer-0123456789ab"
	for _, name := range []string{"cert-manager", "bedrock-system"} {
		if err := client.IgnoreAlreadyExists(run.c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})); err != nil {
			t.Fatal(err)
		}
	}
	for _, secret := range []*corev1.Secret{apiserver.CASecret(certPEM, keyPEM), operator.WebhookTokenSecret(bearer)} {
		if err := run.c.Create(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	markRunning(run)
	runPreparedInit(t, run)
	saved := bootstrapFilesIn(t, filepath.Join(run.root, "var", "lib", "bedrock", "authn"))
	if saved["ca.key"] != string(keyPEM) || saved["ca.crt"] != string(certPEM) || saved["webhook-token"] != bearer {
		t.Fatal("init must take the material the operator created")
	}
	config, err := clientcmd.Load([]byte(readInitFile(t, authnPaths(run.root)[1])))
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthInfos["kube-apiserver"].Token != bearer {
		t.Fatal("the webhook file must carry the cluster bearer")
	}
}

func TestInitRefusesOtherBootstrapOnARunningCluster(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	before := authnHostFiles(t, run.root)
	bootstrap := filepath.Join(run.root, "var", "lib", "bedrock", "authn")
	if err := os.RemoveAll(bootstrap); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrBootstrapAuthn(bootstrap, rand.Reader, time.Now(), "cloud.example.com"); err != nil {
		t.Fatal(err)
	}
	markRunning(run)
	var out, errOut bytes.Buffer
	if code := RunInit(context.Background(), run.args, run.deps, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "cert-manager/bedrock-ca") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if !reflect.DeepEqual(authnHostFiles(t, run.root), before) {
		t.Fatal("the authn host files must stay")
	}
}

func TestInitCreatesAuthnSecrets(t *testing.T) {
	run := prepareInit(t)
	stdout := runPreparedInit(t, run)
	ctx := context.Background()
	bootstrap := filepath.Join(run.root, "var", "lib", "bedrock", "authn")
	var ca corev1.Secret
	if err := run.c.Get(ctx, client.ObjectKey{Namespace: "cert-manager", Name: "bedrock-ca"}, &ca); err != nil {
		t.Fatal(err)
	}
	if ca.Type != corev1.SecretTypeTLS || string(ca.Data["ca.crt"]) != readInitFile(t, filepath.Join(bootstrap, "ca.crt")) || string(ca.Data["tls.key"]) != readInitFile(t, filepath.Join(bootstrap, "ca.key")) {
		t.Fatalf("CA secret %s %v", ca.Type, ca.Labels)
	}
	var token corev1.Secret
	if err := run.c.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "bedrock-authn-webhook-token"}, &token); err != nil {
		t.Fatal(err)
	}
	if token.Labels["bedrock.cloudyfolks.io/authn"] != "true" || len(token.Data["token"]) != 32 {
		t.Fatalf("token secret labels %v length %d", token.Labels, len(token.Data["token"]))
	}
	config, err := clientcmd.Load([]byte(readInitFile(t, authnPaths(run.root)[1])))
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthInfos["kube-apiserver"].Token != string(token.Data["token"]) {
		t.Fatal("the webhook file and the Secret must hold the same bearer")
	}
	for _, written := range []corev1.Secret{ca, token} {
		if len(written.ManagedFields) != 1 || written.ManagedFields[0].Manager != v1alpha1.AuthnFieldManager {
			t.Fatalf("%s field managers %v", written.Name, written.ManagedFields)
		}
	}
	if strings.Contains(stdout, string(token.Data["token"])) || strings.Contains(stdout, "PRIVATE KEY") {
		t.Fatal("init must not print the bearer or the CA key")
	}
	for _, name := range []string{"ca.crt", "ca.key", "webhook-token"} {
		if info, err := os.Stat(filepath.Join(bootstrap, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", name, info, err)
		}
	}
}

func TestK0sConfigHasAuthnArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "k0s", "k0s.yaml")
	var cfg v1alpha1.ClusterConfig
	cfg.Spec.API.VIP = "10.0.10.10"
	cfg.Spec.Network.Fabric.PodCIDR = "10.16.0.0/16"
	cfg.Spec.Network.Fabric.ServiceCIDR = "10.96.0.0/12"
	if err := writeK0sConfig(path, cfg, nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := sigyaml.Unmarshal([]byte(readInitFile(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	args := doc["spec"].(map[string]any)["api"].(map[string]any)["extraArgs"].(map[string]any)
	for name, value := range apiserver.Args() {
		if args[name] != value {
			t.Fatalf("extraArgs %v lack %s=%s", args, name, value)
		}
	}
	if args["default-not-ready-toleration-seconds"] != "30" {
		t.Fatalf("existing extraArgs must stay: %v", args)
	}
}

func TestAuthnFileInputsByMode(t *testing.T) {
	in := apiserver.Inputs{Host: "lab.example", CA: []byte("CA"), Bearer: "b"}
	if got := authnFileInputs("SelfSigned", in); !reflect.DeepEqual(got, in) {
		t.Fatalf("SelfSigned %+v", got)
	}
	for _, mode := range []string{"LetsEncrypt", "Custom"} {
		if got := authnFileInputs(mode, in); got.CA != nil || got.Host != "lab.example" || got.Bearer != "b" {
			t.Fatalf("%s %+v", mode, got)
		}
	}
}

func TestLoadOrBootstrapAuthnKeepsSavedMaterial(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "var", "lib", "bedrock", "authn")
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	first, firstKey, err := loadOrBootstrapAuthn(dir, rand.Reader, now, "lab.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.CA) == 0 || len(firstKey) == 0 || len(first.Bearer) != 32 || first.Host != "lab.example" {
		t.Fatalf("bootstrap %+v", first)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", info, err)
	}
	second, secondKey, err := loadOrBootstrapAuthn(dir, rand.Reader, now, "other.example")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second.CA, first.CA) || !bytes.Equal(secondKey, firstKey) || second.Bearer != first.Bearer || second.Host != "other.example" {
		t.Fatal("a saved CA and bearer must be kept")
	}
}

func TestLoadOrBootstrapAuthnRefusesIncompleteMaterial(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "authn")
	if _, _, err := loadOrBootstrapAuthn(dir, rand.Reader, time.Now(), "lab.example"); err != nil {
		t.Fatal(err)
	}
	cert := readInitFile(t, filepath.Join(dir, "ca.crt"))
	if err := os.Remove(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrBootstrapAuthn(dir, rand.Reader, time.Now(), "lab.example"); err == nil || !strings.Contains(err.Error(), "ca.key") {
		t.Fatalf("a partial bootstrap dir must not be replaced: %v", err)
	}
	if readInitFile(t, filepath.Join(dir, "ca.crt")) != cert {
		t.Fatal("the saved CA must stay")
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrBootstrapAuthn(dir, rand.Reader, time.Now(), "lab.example"); err == nil {
		t.Fatal("a CA key that does not match the certificate must be refused")
	}
}

func TestCreateAuthnSecretsRefusesOtherMaterial(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()
	if err := createAuthnSecrets(ctx, c, []byte("cert"), []byte("key"), "bearer"); err != nil {
		t.Fatal(err)
	}
	if err := createAuthnSecrets(ctx, c, []byte("cert"), []byte("key"), "bearer"); err != nil {
		t.Fatalf("the same material is kept: %v", err)
	}
	err := createAuthnSecrets(ctx, c, []byte("other-cert"), []byte("other-key"), "bearer")
	if err == nil || !strings.Contains(err.Error(), "cert-manager/bedrock-ca") || strings.Contains(err.Error(), "other-key") {
		t.Fatalf("another CA must be refused: %v", err)
	}
	err = createAuthnSecrets(ctx, c, []byte("cert"), []byte("key"), "other-bearer")
	if err == nil || !strings.Contains(err.Error(), "bedrock-system/bedrock-authn-webhook-token") || strings.Contains(err.Error(), "other-bearer") {
		t.Fatalf("another bearer must be refused: %v", err)
	}
	var ca corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cert-manager", Name: "bedrock-ca"}, &ca); err != nil || string(ca.Data["tls.key"]) != "key" {
		t.Fatalf("the CA Secret must stay: %v", err)
	}
}

func TestInitPrintsAdminPasswordOnce(t *testing.T) {
	run := prepareInit(t)
	first := runPreparedInit(t, run)
	lines := strings.Split(first, "\n")
	printed := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "admin password: ") })
	if printed < 0 || len(strings.TrimPrefix(lines[printed], "admin password: ")) != 20 {
		t.Fatalf("stdout %s", first)
	}
	second := runPreparedInit(t, run)
	if strings.Contains(second, "admin password:") || !strings.Contains(second, "admin exists\n") {
		t.Fatalf("a second init prints no password: %s", second)
	}
}

const kmsSocketEndpoint = "unix:///run/kms/kms.sock"

func initDataDir(run initRun) string {
	return filepath.Join(run.root, "var", "lib", "k0s")
}

func initEncryptionPath(run initRun) string {
	return filepath.Join(run.root, encryption.File)
}

func initK0sConfigPath(run initRun) string {
	return filepath.Join(run.root, "etc", "k0s", "k0s.yaml")
}

func encryptionArg(t *testing.T, run initRun) string {
	t.Helper()
	arg, err := k0s.APIExtraArg([]byte(readInitFile(t, initK0sConfigPath(run))), encryption.ArgName)
	if err != nil {
		t.Fatal(err)
	}
	return arg
}

func writeInitK0sConfig(t *testing.T, run initRun, extra map[string]string) {
	t.Helper()
	raw, err := k0s.RenderConfig(k0s.Config{VIP: "10.0.10.10", SANs: []string{"10.0.10.10"}, PodCIDR: "10.16.0.0/16", ServiceCIDR: "10.96.0.0/12", APIExtraArgs: extra})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(initK0sConfigPath(run)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initK0sConfigPath(run), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeKnownEncryptionFile(t *testing.T, run initRun) []byte {
	t.Helper()
	known, err := encryption.NewConfig(bytes.NewReader(bytes.Repeat([]byte{9}, 32)), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := initEncryptionPath(run)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, known, 0o600); err != nil {
		t.Fatal(err)
	}
	return known
}

func assertNoEncryption(t *testing.T, run initRun) {
	t.Helper()
	if _, err := os.Stat(initEncryptionPath(run)); !os.IsNotExist(err) {
		t.Fatalf("the encryption file must not exist: %v", err)
	}
	if _, err := encryption.ReadSecret(context.Background(), run.c); !apierrors.IsNotFound(err) {
		t.Fatalf("the encryption Secret must not exist: %v", err)
	}
}

func assertEncryptionSecret(t *testing.T, run initRun, want []byte) {
	t.Helper()
	got, err := encryption.ReadSecret(context.Background(), run.c)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("encryption Secret %q %v, want %q", got, err, want)
	}
}

func TestRunInitWritesEncryptionFile(t *testing.T) {
	run := prepareInit(t)
	path := initEncryptionPath(run)
	present := false
	run.deps.Exec = filesAtStart{FakeExec: run.deps.Exec.(*host.FakeExec), paths: []string{path}, present: &present}
	stdout := runPreparedInit(t, run)
	if !present {
		t.Fatal("the encryption file must exist before k0s starts")
	}
	if !strings.Contains(stdout, "==> secrets encryption "+encryption.File+"\n") {
		t.Fatalf("stdout lacks the encryption step: %s", stdout)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("encryption file %v %v", info, err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("encryption dir %v %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(initDataDir(run), "pki", encryption.FileName)); !os.IsNotExist(err) {
		t.Fatalf("the key must stay out of the k0s pki directory: %v", err)
	}
	content := []byte(readInitFile(t, path))
	sockets, err := encryption.SocketPaths(content)
	if err != nil || len(sockets) != 0 || !strings.Contains(string(content), "aesgcm") {
		t.Fatalf("want an aesgcm config without sockets, got %q %v %v", content, sockets, err)
	}
	if got := encryptionArg(t, run); got != encryption.File {
		t.Fatalf("k0s.yaml names %q, want %q", got, encryption.File)
	}
	assertEncryptionSecret(t, run, content)
}

func TestRunInitKeepsEncryptionKey(t *testing.T) {
	run := prepareInit(t)
	known := writeKnownEncryptionFile(t, run)
	runPreparedInit(t, run)
	if got := readInitFile(t, initEncryptionPath(run)); got != string(known) {
		t.Fatalf("init replaced the key: %q", got)
	}
	assertEncryptionSecret(t, run, known)
}

func TestRunInitRerunKeepsTheGeneratedKey(t *testing.T) {
	run := prepareInit(t)
	runPreparedInit(t, run)
	first := readInitFile(t, initEncryptionPath(run))
	runPreparedInit(t, run)
	markRunning(run)
	runPreparedInit(t, run)
	if got := readInitFile(t, initEncryptionPath(run)); got != first {
		t.Fatalf("a re-run replaced the key: %q", got)
	}
	assertEncryptionSecret(t, run, []byte(first))
}

func TestRunInitRunningWithoutEncryption(t *testing.T) {
	run := prepareInit(t)
	writeInitK0sConfig(t, run, nil)
	markRunning(run)
	runPreparedInit(t, run)
	assertNoEncryption(t, run)
	if got := encryptionArg(t, run); got != "" {
		t.Fatalf("k0s.yaml names %q, want none", got)
	}
}

func TestRunInitRunningLostEncryptionFile(t *testing.T) {
	run := prepareInit(t)
	writeInitK0sConfig(t, run, encryption.Args())
	markRunning(run)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "copy it from another control-plane node or from your separate key copy, or read Secret kube-system/bedrock-secrets-encryption through another control-plane node") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	assertNoEncryption(t, run)
}

func TestRunInitSecretMismatch(t *testing.T) {
	run := prepareInit(t)
	if err := run.c.Create(context.Background(), encryption.Secret([]byte("other"))); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "holds another encryption config") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	assertEncryptionSecret(t, run, []byte("other"))
}

func useKMSConfig(t *testing.T, run initRun) {
	t.Helper()
	if err := os.WriteFile(run.args[1], []byte(initConfig+"  encryption:\n    kms:\n      endpoint: "+kmsSocketEndpoint+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func statWithSocket(run initRun, mode fs.FileMode) func(string) (fs.FileInfo, error) {
	socket := filepath.Join(run.root, "run", "kms", "kms.sock")
	others := run.deps.Stat
	return func(path string) (fs.FileInfo, error) {
		if path != socket {
			return others(path)
		}
		if mode == 0 {
			return nil, fs.ErrNotExist
		}
		return devInfo{mode}, nil
	}
}

func TestRunInitKMSNeedsSocket(t *testing.T) {
	run := prepareInit(t)
	useKMSConfig(t, run)
	run.deps.Stat = statWithSocket(run, 0)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "kms plugin socket /run/kms/kms.sock is missing") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if slices.Contains(run.deps.Exec.(*host.FakeExec).Calls, "/usr/local/bin/k0s start") {
		t.Fatal("k0s must not start without the KMS socket")
	}
}

func TestRunInitKMSWritesTheProviderOnly(t *testing.T) {
	run := prepareInit(t)
	useKMSConfig(t, run)
	run.deps.Stat = statWithSocket(run, fs.ModeSocket)
	runPreparedInit(t, run)
	content := []byte(readInitFile(t, initEncryptionPath(run)))
	sockets, err := encryption.SocketPaths(content)
	if err != nil || !reflect.DeepEqual(sockets, []string{"/run/kms/kms.sock"}) || strings.Contains(string(content), "aesgcm") {
		t.Fatalf("want a kms config on the socket, got %q %v %v", content, sockets, err)
	}
	assertEncryptionSecret(t, run, content)
}

func makeEtcdDir(t *testing.T, run initRun) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(initDataDir(run), "etcd"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestRunInitRunningRefusesAnUnnamedEncryptionFile(t *testing.T) {
	run := prepareInit(t)
	known := writeKnownEncryptionFile(t, run)
	writeInitK0sConfig(t, run, nil)
	markRunning(run)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
	path := encryption.File
	want := path + " exists but k0s.yaml does not name it: add encryption-provider-config=" + path + " back to the api extraArgs of k0s.yaml if this node ever ran encrypted, remove the file only if it never did"
	if code != 1 || !strings.Contains(errOut.String(), want) {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if got := readInitFile(t, initEncryptionPath(run)); got != string(known) {
		t.Fatalf("init changed the file: %q", got)
	}
	if got := encryptionArg(t, run); got != "" {
		t.Fatalf("k0s.yaml names %q, want none", got)
	}
}

func TestRunInitRunningRefusesAnotherEncryptionPath(t *testing.T) {
	run := prepareInit(t)
	known := writeKnownEncryptionFile(t, run)
	other := "/var/lib/k0s/pki/encryption.yaml"
	writeInitK0sConfig(t, run, map[string]string{encryption.ArgName: other})
	configBefore := readInitFile(t, initK0sConfigPath(run))
	markRunning(run)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
	want := "k0s.yaml names " + other + " as the encryption config, not " + encryption.File
	if code != 1 || !strings.Contains(errOut.String(), want) {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if got := readInitFile(t, initEncryptionPath(run)); got != string(known) {
		t.Fatalf("init changed the file: %q", got)
	}
	if got := readInitFile(t, initK0sConfigPath(run)); got != configBefore {
		t.Fatalf("init changed k0s.yaml: %q", got)
	}
	if _, err := encryption.ReadSecret(context.Background(), run.c); !apierrors.IsNotFound(err) {
		t.Fatalf("the encryption Secret must not exist: %v", err)
	}
}

func TestRunInitStoppedNodeKeepsLostEncryptionFile(t *testing.T) {
	run := prepareInit(t)
	makeEtcdDir(t, run)
	writeInitK0sConfig(t, run, encryption.Args())
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "copy it from another control-plane node or from your separate key copy, or read Secret kube-system/bedrock-secrets-encryption through another control-plane node") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	assertNoEncryption(t, run)
}

func TestRunInitStoppedNodeWithoutEncryption(t *testing.T) {
	run := prepareInit(t)
	makeEtcdDir(t, run)
	writeInitK0sConfig(t, run, nil)
	runPreparedInit(t, run)
	assertNoEncryption(t, run)
	if got := encryptionArg(t, run); got != "" {
		t.Fatalf("k0s.yaml names %q, want none", got)
	}
}

func TestRunInitRefusesAnUnusableEncryptionFile(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "malformed": "resources: ["} {
		t.Run(name, func(t *testing.T) {
			run := prepareInit(t)
			path := initEncryptionPath(run)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			code := RunInit(context.Background(), run.args, run.deps, &out, &errOut)
			if code != 1 || !strings.Contains(errOut.String(), encryption.File) {
				t.Fatalf("exit %d stderr %s", code, errOut.String())
			}
			if got := readInitFile(t, path); got != content {
				t.Fatalf("init replaced the file: %q", got)
			}
			if _, err := encryption.ReadSecret(context.Background(), run.c); !apierrors.IsNotFound(err) {
				t.Fatalf("the encryption Secret must not exist: %v", err)
			}
		})
	}
}
