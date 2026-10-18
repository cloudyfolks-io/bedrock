package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

func TestJoinRequiresToken(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"join"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("--token")) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestJoinRejectsGarbageToken(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"join", "--token", "###"}, &out, &errOut); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

func joinToken(t *testing.T) string {
	t.Helper()
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: []string{"workload"}, K0sToken: "tok",
		VIP: "10.0.10.10", K0sVersion: "1.36.3+k0s.0", SupportedOS: []string{"ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRunJoinInstallsWhenNotRunning(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	tokenPath := filepath.Join(root, "etc", "k0s", "join-token")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{Role: "worker", Force: true, TokenFile: tokenPath, Labels: roles.Labels([]string{"workload"}), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	executable := fakeExecutable(t)
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: executable}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", joinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("joined as workload")) {
		t.Fatalf("stdout %s", out.String())
	}
	installed := false
	for _, call := range e.Calls {
		if call == "/usr/local/bin/k0s "+strings.Join(installArgs, " ") {
			installed = true
		}
	}
	if !installed {
		t.Fatal("expected k0s install to run when not already running")
	}
	assertAgentInstalled(t, root, dataDir, executable)
	assertDeviceOwnership(t, root)
}

func TestRunJoinAcceptsAVIPThatAnswersPing(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	delete(e.Errors, "ping -c 1 -W 1 10.0.10.10")
	e.Responses["ping -c 1 -W 1 10.0.10.10"] = ""
	tokenPath := filepath.Join(root, "etc", "k0s", "join-token")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{Role: "worker", Force: true, TokenFile: tokenPath, Labels: roles.Labels([]string{"workload"}), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t)}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", joinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if bytes.Contains(out.Bytes(), []byte("vip-free")) {
		t.Fatalf("join must not check that the vip is free: %s", out.String())
	}
}

func controlPlaneJoinToken(t *testing.T) string {
	t.Helper()
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: []string{"control-plane"}, K0sToken: "tok",
		K0sConfig:  []byte("apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\n"),
		AuthnFiles: map[string][]byte{"authentication.yaml": []byte("authn-config"), "webhook.kubeconfig": []byte("webhook-config")},
		VIP:        "10.0.10.10", K0sVersion: "1.36.3+k0s.0", SupportedOS: []string{"ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRunJoinRejectsControlPlaneWithoutConfig(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: []string{"control-plane"}, K0sToken: "tok",
		VIP: "10.0.10.10", K0sVersion: "1.36.3+k0s.0", SupportedOS: []string{"ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	executable := fakeExecutable(t)
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: executable}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", token, "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "k0s.yaml")); !os.IsNotExist(err) {
		t.Fatal("k0s.yaml must not be written without a k0s config")
	}
	for _, call := range e.Calls {
		if strings.Contains(call, "k0s install") {
			t.Fatal("k0s must not be installed without a k0s config")
		}
	}
}

func TestRunJoinControlPlaneEnablesWorkerWithoutWorkloadRole(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	configPath := filepath.Join(root, "etc", "k0s", "k0s.yaml")
	tokenPath := filepath.Join(root, "etc", "k0s", "join-token")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: configPath, TokenFile: tokenPath,
		EnableWorker: true, NoTaints: true, DynamicConfig: true, Labels: roles.Labels([]string{"control-plane"}),
		KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir, DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	executable := fakeExecutable(t)
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: executable, NewClient: clusterWith(t)}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", controlPlaneJoinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	installed := false
	for _, call := range e.Calls {
		if call == "/usr/local/bin/k0s "+strings.Join(installArgs, " ") {
			installed = true
		}
	}
	if !installed {
		t.Fatal("expected controller install to enable the kubelet")
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
}

func TestRunJoinFromBundle(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	workDir := filepath.Join(root, "var", "lib", "bedrock")
	k0sBin := filepath.Join(root, "usr", "local", "bin", "k0s")
	bundlePath, sum := buildFixtureBundle(t, "v0.1.0-test", "v1.99.0+k0s.0")
	e.Errors[k0sBin+" status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	tokenPath := filepath.Join(root, "etc", "k0s", "join-token")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{Role: "worker", Force: true, TokenFile: tokenPath, Labels: roles.Labels([]string{"workload"}), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir})
	e.Responses[k0sBin+" "+strings.Join(installArgs, " ")] = ""
	e.Responses[k0sBin+" start"] = ""
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: []string{"workload"}, K0sToken: "tok",
		VIP: "10.0.10.10", K0sVersion: "v1.99.0+k0s.0", K0sChecksums: map[string]string{fixtureArch: sum},
		SupportedOS: []string{"ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	executable := fakeExecutable(t)
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: executable}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", token, "--data-dir", dataDir, "--k0s-bin", k0sBin, "--bundle", bundlePath, "--work-dir", workDir}, deps, &out, &errOut)
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
		t.Fatal("extracted bundle dir must be removed after a successful join")
	}
}

func TestRunJoinSkipsInstallWhenAlreadyRunning(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	executable := fakeExecutable(t)
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: executable}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", joinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	for _, call := range e.Calls {
		if strings.Contains(call, "k0s install") || call == "/usr/local/bin/k0s start" {
			t.Fatalf("k0s must not be reinstalled or restarted when already running, got %q", call)
		}
	}
}

func TestRunJoinWritesMirror(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	tokenPath := filepath.Join(root, "etc", "k0s", "join-token")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{Role: "worker", Force: true, TokenFile: tokenPath, Labels: roles.Labels([]string{"workload"}), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: []string{"workload"}, K0sToken: "tok",
		VIP: "10.0.10.10", K0sVersion: "1.36.3+k0s.0", SupportedOS: []string{"ubuntu-24.04"},
		Mirror: "https://m.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t)}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", token, "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "etc", "k0s", "containerd.d", "certs.d", "_default", "hosts.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "https://m.example") {
		t.Fatalf("hosts.toml %q", got)
	}
}

type filesAtStart struct {
	*host.FakeExec
	paths   []string
	present *bool
}

func (f filesAtStart) Run(ctx context.Context, name string, args ...string) (string, error) {
	if strings.HasSuffix(strings.TrimSpace(name+" "+strings.Join(args, " ")), "k0s start") {
		*f.present = allExist(f.paths)
	}
	return f.FakeExec.Run(ctx, name, args...)
}

func allExist(paths []string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func authnPaths(root string) []string {
	return []string{filepath.Join(root, "etc", "bedrock", "authn", "authentication.yaml"), filepath.Join(root, "etc", "bedrock", "authn", "webhook.kubeconfig")}
}

func TestJoinWritesAuthnFiles(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: filepath.Join(root, "etc", "k0s", "k0s.yaml"), TokenFile: filepath.Join(root, "etc", "k0s", "join-token"),
		EnableWorker: true, NoTaints: true, DynamicConfig: true, Labels: roles.Labels([]string{"control-plane"}),
		KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir, DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	present := false
	exec := filesAtStart{FakeExec: e, paths: authnPaths(root), present: &present}
	deps := InitDeps{Exec: exec, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t), NewClient: clusterWith(t)}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", controlPlaneJoinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !present {
		t.Fatal("the authn files must exist before k0s starts")
	}
	for path, want := range map[string]string{authnPaths(root)[0]: "authn-config", authnPaths(root)[1]: "webhook-config"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q %v", path, got, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", path, info, err)
		}
	}
}

func TestJoinRefusesControllerTokenWithoutAuthnFiles(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: []string{"control-plane"}, K0sToken: "tok",
		K0sConfig: []byte("apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\n"),
		VIP:       "10.0.10.10", K0sVersion: "1.36.3+k0s.0", SupportedOS: []string{"ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t)}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", token, "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "authentication.yaml") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "k0s.yaml")); !os.IsNotExist(err) {
		t.Fatal("k0s.yaml must not be written without the authn files")
	}
	for _, call := range e.Calls {
		if strings.Contains(call, "k0s install") {
			t.Fatal("k0s must not be installed without the authn files")
		}
	}
}

func TestCheckAuthnFilesRefusesOtherNames(t *testing.T) {
	complete := map[string][]byte{"authentication.yaml": []byte("a"), "webhook.kubeconfig": []byte("w")}
	if err := checkAuthnFiles(complete); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../../usr/local/bin/k0s", "ca.key"} {
		files := map[string][]byte{"authentication.yaml": []byte("a"), "webhook.kubeconfig": []byte("w"), name: []byte("x")}
		if err := checkAuthnFiles(files); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("a token file named %q must be refused: %v", name, err)
		}
	}
	if err := checkAuthnFiles(map[string][]byte{"authentication.yaml": []byte("a"), "webhook.kubeconfig": nil}); err == nil || !strings.Contains(err.Error(), "webhook.kubeconfig") {
		t.Fatalf("an empty webhook file must be refused: %v", err)
	}
}

func fakeCluster(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func clusterWith(t *testing.T, objects ...client.Object) func(string) (client.Client, error) {
	t.Helper()
	c := fakeCluster(t, objects...)
	return func(string) (client.Client, error) { return c, nil }
}

func controllerJoinHost(t *testing.T) (string, string, *host.FakeExec) {
	t.Helper()
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	installArgs := k0s.InstallArgs(k0s.InstallOptions{
		Role: "controller", Force: true, ConfigPath: filepath.Join(root, "etc", "k0s", "k0s.yaml"), TokenFile: filepath.Join(root, "etc", "k0s", "join-token"),
		EnableWorker: true, NoTaints: true, DynamicConfig: true, Labels: roles.Labels([]string{"control-plane"}),
		KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir, DisableComponents: k0s.DefaultDisabledComponents,
	})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	return root, dataDir, e
}

func clusterAuthnSecretsFor(t *testing.T, bearer string) ([]byte, []byte, []client.Object) {
	t.Helper()
	certPEM, keyPEM, err := apiserver.NewCA(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, []client.Object{apiserver.CASecret(certPEM, keyPEM), operator.WebhookTokenSecret(bearer)}
}

func TestJoinRestoresAuthnBootstrapFromTheCluster(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	certPEM, keyPEM, secrets := clusterAuthnSecretsFor(t, "cluster-bearer")
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t), NewClient: clusterWith(t, secrets...)}
	var out, errOut bytes.Buffer
	if code := RunJoin(context.Background(), []string{"--token", controlPlaneJoinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	dir := filepath.Join(root, "var", "lib", "bedrock", "authn")
	for name, want := range map[string]string{"ca.crt": string(certPEM), "ca.key": string(keyPEM), "webhook-token": "cluster-bearer"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q %v", name, got, err)
		}
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", name, info, err)
		}
	}
}

func TestJoinWithoutClusterMaterialLeavesNoBootstrap(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t), NewClient: clusterWith(t)}
	var out, errOut bytes.Buffer
	if code := RunJoin(context.Background(), []string{"--token", controlPlaneJoinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "var", "lib", "bedrock", "authn")); !os.IsNotExist(err) {
		t.Fatalf("a join must not make authn material: %v", err)
	}
}

func TestJoinRefusesOtherAuthnBootstrap(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	_, _, secrets := clusterAuthnSecretsFor(t, "cluster-bearer")
	dir := filepath.Join(root, "var", "lib", "bedrock", "authn")
	if _, _, err := loadOrBootstrapAuthn(dir, rand.Reader, time.Now(), "lab.example"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t), NewClient: clusterWith(t, secrets...)}
	var out, errOut bytes.Buffer
	if code := RunJoin(context.Background(), []string{"--token", controlPlaneJoinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "cert-manager/bedrock-ca") {
		t.Fatalf("exit %d stderr %s", code, errOut.String())
	}
	if after, err := os.ReadFile(filepath.Join(dir, "ca.key")); err != nil || string(after) != string(before) {
		t.Fatal("the host material must stay")
	}
}

func startJoinAPI(t *testing.T, status int, ca k0s.CA) (string, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if status != http.StatusOK {
			http.Error(w, "invalid token", status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string][]byte{"cert": ca.Cert, "key": ca.Key})
	}))
	t.Cleanup(srv.Close)
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: k0s\n  cluster:\n    server: %s\n    certificate-authority-data: %s\ncontexts:\n- name: k0s\n  context:\n    cluster: k0s\n    user: controller-join\ncurrent-context: k0s\nusers:\n- name: controller-join\n  user:\n    token: abcdef.0123456789abcdef\n",
		srv.URL, base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	if _, err := writer.Write([]byte(kubeconfig)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(zipped.Bytes()), requests
}

func newJoinCA(t *testing.T) k0s.CA {
	t.Helper()
	certPEM, keyPEM, err := apiserver.NewCA(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return k0s.CA{Cert: certPEM, Key: keyPEM}
}

var testEncryptionKey = bytes.Repeat([]byte{7}, 32)

func testEncryptionContent(t *testing.T) []byte {
	t.Helper()
	content, err := encryption.NewConfig(bytes.NewReader(testEncryptionKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func encryptionJoinToken(t *testing.T, nodeRoles []string, k0sToken string, apiArgs map[string]string) string {
	t.Helper()
	config, err := k0s.RenderConfig(k0s.Config{VIP: "10.0.10.10", SANs: []string{"10.0.10.10"}, PodCIDR: "10.16.0.0/16", ServiceCIDR: "10.96.0.0/12", APIExtraArgs: apiArgs})
	if err != nil {
		t.Fatal(err)
	}
	token, err := k0s.EncodeToken(k0s.Token{
		Version: "v0.1.0-test", Roles: nodeRoles, K0sToken: k0sToken, K0sConfig: config,
		AuthnFiles: map[string][]byte{"authentication.yaml": []byte("authn-config"), "webhook.kubeconfig": []byte("webhook-config")},
		VIP:        "10.0.10.10", K0sVersion: "1.36.3+k0s.0", SupportedOS: []string{"ubuntu-24.04"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func configClient(t *testing.T, seen *[]*rest.Config, objects ...client.Object) func(*rest.Config) (client.Client, error) {
	t.Helper()
	c := fakeCluster(t, objects...)
	return func(config *rest.Config) (client.Client, error) {
		*seen = append(*seen, config)
		return c, nil
	}
}

func runJoinWithToken(t *testing.T, root, dataDir string, exec host.Exec, token string, newConfigClient func(*rest.Config) (client.Client, error)) (int, string, string) {
	t.Helper()
	deps := InitDeps{Exec: exec, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t), NewClient: clusterWith(t), NewConfigClient: newConfigClient}
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), []string{"--token", token, "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s"}, deps, &out, &errOut)
	return code, out.String(), errOut.String()
}

func k0sInstallCalled(e *host.FakeExec) bool {
	for _, call := range e.Calls {
		if strings.Contains(call, "k0s install") {
			return true
		}
	}
	return false
}

func TestRunJoinFetchesEncryptionFile(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	content := testEncryptionContent(t)
	k0sToken, requests := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	token := encryptionJoinToken(t, []string{"control-plane"}, k0sToken, encryption.Args())
	path := filepath.Join(root, encryption.File)
	present := false
	var seen []*rest.Config
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, filesAtStart{FakeExec: e, paths: []string{path}, present: &present}, token, configClient(t, &seen, encryption.Secret(content)))
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if !present {
		t.Fatal("the encryption file must exist before k0s starts")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("encryption file %q %v, want %q", got, err, content)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("encryption file %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("encryption dir %v %v", info, err)
	}
	if !strings.Contains(stdout, "==> secrets encryption\n") {
		t.Fatalf("stdout lacks the encryption step: %s", stdout)
	}
	if requests.Load() != 1 || len(seen) != 1 {
		t.Fatalf("join api requests %d, secret clients %d", requests.Load(), len(seen))
	}
	if seen[0].Host != "https://10.0.10.10:6443" {
		t.Fatalf("secret read host %q", seen[0].Host)
	}
	block, _ := pem.Decode(seen[0].CertData)
	if block == nil {
		t.Fatalf("join client certificate is not PEM: %q", seen[0].CertData)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || cert.Subject.CommonName != encryption.JoinUser {
		t.Fatalf("join certificate %v %v", cert, err)
	}
}

func TestRunJoinRefusesAnotherEncryptionPath(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	k0sToken, requests := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	token := encryptionJoinToken(t, []string{"control-plane"}, k0sToken, map[string]string{encryption.ArgName: "/var/lib/k0s/pki/encryption.yaml"})
	var seen []*rest.Config
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, e, token, configClient(t, &seen))
	want := "the cluster keeps its encryption config at /var/lib/k0s/pki/encryption.yaml, this bedrock version expects /etc/bedrock/encryption/encryption.yaml"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if requests.Load() != 0 || len(seen) != 0 || len(e.Calls) != 0 {
		t.Fatalf("join api requests %d, secret clients %d, calls %v", requests.Load(), len(seen), e.Calls)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "k0s.yaml")); !os.IsNotExist(err) {
		t.Fatal("k0s.yaml must not be written")
	}
}

func TestRunJoinRefusedByJoinAPI(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	k0sToken, _ := startJoinAPI(t, http.StatusUnauthorized, k0s.CA{})
	token := encryptionJoinToken(t, []string{"control-plane"}, k0sToken, encryption.Args())
	var seen []*rest.Config
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, e, token, configClient(t, &seen))
	if code != 1 || !strings.Contains(stderr, "fetch the cluster CA") || !strings.Contains(stderr, "401") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if k0sInstallCalled(e) || len(seen) != 0 {
		t.Fatalf("k0s must not be installed after a refused join: calls %v", e.Calls)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "k0s.yaml")); !os.IsNotExist(err) {
		t.Fatal("k0s.yaml must not be written after a refused join")
	}
}

func TestRunJoinMissingEncryptionSecret(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	k0sToken, _ := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	token := encryptionJoinToken(t, []string{"control-plane"}, k0sToken, encryption.Args())
	var seen []*rest.Config
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, e, token, configClient(t, &seen))
	if code != 1 || !strings.Contains(stderr, "kube-system/bedrock-secrets-encryption") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if k0sInstallCalled(e) {
		t.Fatalf("k0s must not be installed without the encryption secret: calls %v", e.Calls)
	}
	if _, err := os.Stat(filepath.Join(root, encryption.File)); !os.IsNotExist(err) {
		t.Fatalf("no encryption file may exist: %v", err)
	}
}

func TestRunJoinWithoutEncryption(t *testing.T) {
	root, dataDir, e := controllerJoinHost(t)
	k0sToken, requests := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	token := encryptionJoinToken(t, []string{"control-plane"}, k0sToken, nil)
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, e, token, nil)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if requests.Load() != 0 {
		t.Fatalf("join api requests %d, want none", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, encryption.File)); !os.IsNotExist(err) {
		t.Fatalf("no encryption file may exist: %v", err)
	}
}

func TestRunJoinWorkerSkipsEncryption(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	installArgs := k0s.InstallArgs(k0s.InstallOptions{Role: "worker", Force: true, TokenFile: filepath.Join(root, "etc", "k0s", "join-token"), Labels: roles.Labels([]string{"workload"}), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	k0sToken, requests := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	token := encryptionJoinToken(t, []string{"workload"}, k0sToken, encryption.Args())
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, e, token, nil)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if requests.Load() != 0 {
		t.Fatalf("join api requests %d, want none", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, encryption.File)); !os.IsNotExist(err) {
		t.Fatalf("no encryption file may exist: %v", err)
	}
}

func TestRunJoinRunningNodeSkipsEncryptionFetch(t *testing.T) {
	root, e := fakeHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	k0sToken, requests := startJoinAPI(t, http.StatusUnauthorized, k0s.CA{})
	token := encryptionJoinToken(t, []string{"control-plane"}, k0sToken, encryption.Args())
	code, stdout, stderr := runJoinWithToken(t, root, dataDir, e, token, nil)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if requests.Load() != 0 {
		t.Fatalf("join api requests %d, want none", requests.Load())
	}
}
