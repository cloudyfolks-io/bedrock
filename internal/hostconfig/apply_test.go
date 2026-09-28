package hostconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/pkgmgr"
)

func stateOf(steps []v1alpha1.StepResult, name string) v1alpha1.StepResult {
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	return v1alpha1.StepResult{}
}

func TestApplyRunsEveryStep(t *testing.T) {
	root := t.TempDir()
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"sysctl -p ": ""}, Responses: map[string]string{
		"dnf install -y qemu-kvm":             "",
		"modprobe kvm":                        "",
		"modprobe vhost_net":                  "",
		"systemctl daemon-reload":             "",
		"systemctl enable --now demo.service": "",
	}}
	spec := v1alpha1.HostConfigSpec{
		Packages:          []string{"qemu-kvm"},
		Modules:           []string{"kvm", "vhost_net"},
		Sysctls:           map[string]string{"net.ipv4.ip_forward": "1", "fs.inotify.max_user_watches": "1048576"},
		KernelCmdline:     []string{"intel_iommu=on"},
		Units:             []v1alpha1.UnitSpec{{Name: "demo.service", Content: "[Unit]\nDescription=demo\n", Enabled: true}},
		ContainerdMirrors: []v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: "https://mirror.example"}, {Registry: "quay.io", Endpoint: "https://quay-mirror.example"}},
	}
	steps := Apply(context.Background(), Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "dnf", Root: root}}, spec)
	if Failed(steps) {
		t.Fatalf("steps %+v", steps)
	}
	for _, name := range []string{"packages", "modules", "sysctls", "units", "containerdMirrors"} {
		if stateOf(steps, name).State != "Applied" {
			t.Fatalf("%s: %+v", name, stateOf(steps, name))
		}
	}
	if stateOf(steps, "kernelCmdline").State != "Skipped" || stateOf(steps, "nmstate").State != "Skipped" {
		t.Fatalf("skipped steps %+v", steps)
	}
	modules, _ := os.ReadFile(filepath.Join(root, "etc", "modules-load.d", "bedrock.conf"))
	if string(modules) != "kvm\nvhost_net\n" {
		t.Fatalf("modules file %q", modules)
	}
	sysctl, _ := os.ReadFile(filepath.Join(root, "etc", "sysctl.d", "90-bedrock.conf"))
	if string(sysctl) != "fs.inotify.max_user_watches = 1048576\nnet.ipv4.ip_forward = 1\n" {
		t.Fatalf("sysctl file %q", sysctl)
	}
	unit, _ := os.ReadFile(filepath.Join(root, "etc", "systemd", "system", "demo.service"))
	if !strings.Contains(string(unit), "Description=demo") {
		t.Fatalf("unit %q", unit)
	}
	hosts, _ := os.ReadFile(filepath.Join(root, "etc", "k0s", "containerd.d", "certs.d", "quay.io", "hosts.toml"))
	if !strings.Contains(string(hosts), `[host."https://quay-mirror.example"]`) {
		t.Fatalf("hosts.toml %q", hosts)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "k0s", "containerd.d", "certs.d", "_default", "hosts.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyContinuesAfterFailure(t *testing.T) {
	root := t.TempDir()
	exec := &host.FakeExec{
		Responses:        map[string]string{"modprobe missing": ""},
		ResponsePrefixes: map[string]string{"sysctl -p ": ""},
		Errors:           map[string]error{"modprobe missing": &host.ExitError{Code: 1}},
	}
	spec := v1alpha1.HostConfigSpec{Modules: []string{"missing"}, Sysctls: map[string]string{"a": "1"}}
	steps := Apply(context.Background(), Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}}, spec)
	if !Failed(steps) {
		t.Fatal("expected failure")
	}
	if stateOf(steps, "modules").State != "Failed" || stateOf(steps, "modules").Message == "" {
		t.Fatalf("modules %+v", stateOf(steps, "modules"))
	}
	if stateOf(steps, "sysctls").State != "Applied" {
		t.Fatalf("sysctls must still run: %+v", stateOf(steps, "sysctls"))
	}
}

func TestApplyTriesEveryModule(t *testing.T) {
	root := t.TempDir()
	exec := &host.FakeExec{
		Responses:        map[string]string{"modprobe kvm": "", "modprobe vhost_net": ""},
		ResponsePrefixes: map[string]string{"sysctl -p ": ""},
		Errors:           map[string]error{"modprobe kvm": &host.ExitError{Code: 1}, "modprobe vhost_net": &host.ExitError{Code: 1}},
	}
	spec := v1alpha1.HostConfigSpec{Modules: []string{"kvm", "vhost_net"}, Sysctls: map[string]string{"a": "1"}}
	steps := Apply(context.Background(), Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}}, spec)
	modules := stateOf(steps, "modules")
	if modules.State != "Failed" {
		t.Fatalf("modules %+v", modules)
	}
	if modules.Message != "kvm: exit status 1; vhost_net: exit status 1" {
		t.Fatalf("message %q", modules.Message)
	}
	if len(exec.Calls) < 2 || exec.Calls[0] != "modprobe kvm" || exec.Calls[1] != "modprobe vhost_net" {
		t.Fatalf("calls %v", exec.Calls)
	}
	if stateOf(steps, "sysctls").State != "Applied" {
		t.Fatalf("sysctls must still run: %+v", stateOf(steps, "sysctls"))
	}
}

func TestApplyDisablesUnits(t *testing.T) {
	root := t.TempDir()
	exec := &host.FakeExec{Responses: map[string]string{"systemctl daemon-reload": "", "systemctl disable --now old.service": ""}, ResponsePrefixes: map[string]string{"sysctl -p ": ""}}
	steps := Apply(context.Background(), Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}}, v1alpha1.HostConfigSpec{Units: []v1alpha1.UnitSpec{{Name: "old.service", Content: "[Unit]\n", Enabled: false}}})
	if stateOf(steps, "units").State != "Applied" {
		t.Fatalf("units %+v", stateOf(steps, "units"))
	}
}

func TestApplyRestartsChangedEnabledUnits(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "systemd", "system")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"app.service": "old", "same.service": "same", "off.service": "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"systemctl ": "", "sysctl -p ": ""}}
	units := []v1alpha1.UnitSpec{{Name: "app.service", Content: "new", Enabled: true}, {Name: "same.service", Content: "same", Enabled: true}, {Name: "off.service", Content: "new", Enabled: false}}
	steps := Apply(context.Background(), Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}}, v1alpha1.HostConfigSpec{Units: units})
	if stateOf(steps, "units").State != "Applied" {
		t.Fatalf("units %+v", stateOf(steps, "units"))
	}
	restarts := []string{}
	for _, call := range exec.Calls {
		if strings.HasPrefix(call, "systemctl restart ") {
			restarts = append(restarts, call)
		}
	}
	if len(restarts) != 1 || restarts[0] != "systemctl restart app.service" {
		t.Fatalf("only the changed enabled unit may restart, got %v", restarts)
	}
}

func TestApplyRemovesMirrorsNoLongerWanted(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "etc", "k0s", "containerd.d", "certs.d", "_default", "hosts.toml")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("[host.\"https://old.example\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := &host.FakeExec{ResponsePrefixes: map[string]string{"sysctl -p ": ""}}
	steps := Apply(context.Background(), Deps{Exec: exec, Root: root, Packages: pkgmgr.Manager{Exec: exec, Family: "apt", Root: root}}, v1alpha1.HostConfigSpec{})
	if stateOf(steps, "containerdMirrors").State != "Applied" {
		t.Fatalf("containerdMirrors %+v", stateOf(steps, "containerdMirrors"))
	}
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatalf("a mirror no longer in the HostConfig must be removed, stat error %v", err)
	}
}
