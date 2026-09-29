package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
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
	deps := InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: executable}
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
	deps := InitDeps{Exec: exec, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Root: root, Executable: fakeExecutable(t)}
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
