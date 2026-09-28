package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func TestDetect(t *testing.T) {
	cases := map[string]string{"ubuntu": "apt", "debian": "apt", "fedora": "dnf", "rhel": "dnf", "centos": "dnf", "sles": "zypper", "opensuse-leap": "zypper"}
	for id, want := range cases {
		got, err := Detect(id)
		if err != nil || got != want {
			t.Fatalf("%s: got %s err %v", id, got, err)
		}
	}
	if _, err := Detect("alpine"); err == nil {
		t.Fatal("expected error for unknown family")
	}
}

func TestInstallSkipsEmptyList(t *testing.T) {
	exec := &host.FakeExec{}
	if err := (Manager{Exec: exec, Family: "apt"}).Install(context.Background(), nil); err != nil || len(exec.Calls) != 0 {
		t.Fatalf("err %v calls %v", err, exec.Calls)
	}
}

func TestInstallPerFamily(t *testing.T) {
	cases := map[string]string{
		"apt":    "env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends a b",
		"dnf":    "dnf install -y a b",
		"zypper": "zypper --non-interactive install a b",
	}
	for family, want := range cases {
		exec := &host.FakeExec{Responses: map[string]string{want: ""}}
		if err := (Manager{Exec: exec, Family: family}).Install(context.Background(), []string{"a", "b"}); err != nil {
			t.Fatalf("%s: %v", family, err)
		}
	}
}

func TestSecurityUpdatePerFamily(t *testing.T) {
	cases := map[string][]string{
		"apt":    {"apt-get update", "unattended-upgrade -v"},
		"dnf":    {"dnf upgrade -y --security"},
		"zypper": {"zypper --non-interactive patch --category security"},
	}
	for family, want := range cases {
		responses := map[string]string{}
		for _, call := range want {
			responses[call] = ""
		}
		exec := &host.FakeExec{Responses: responses}
		if err := (Manager{Exec: exec, Family: family}).SecurityUpdate(context.Background()); err != nil {
			t.Fatalf("%s: %v", family, err)
		}
		if len(exec.Calls) != len(want) {
			t.Fatalf("%s: calls %v", family, exec.Calls)
		}
	}
}

func TestRebootRequired(t *testing.T) {
	root := t.TempDir()
	m := Manager{Exec: &host.FakeExec{}, Family: "apt", Root: root}
	if got, _ := m.RebootRequired(context.Background()); got {
		t.Fatal("apt without file must be false")
	}
	os.MkdirAll(filepath.Join(root, "var", "run"), 0o755)
	os.WriteFile(filepath.Join(root, "var", "run", "reboot-required"), nil, 0o644)
	if got, _ := m.RebootRequired(context.Background()); !got {
		t.Fatal("apt with file must be true")
	}
	dnf := Manager{Exec: &host.FakeExec{Errors: map[string]error{"needs-restarting -r": &host.ExitError{Code: 1}}, Responses: map[string]string{"needs-restarting -r": ""}}, Family: "dnf"}
	if got, err := dnf.RebootRequired(context.Background()); err != nil || !got {
		t.Fatalf("dnf exit 1: got %v err %v", got, err)
	}
	zypper := Manager{Exec: &host.FakeExec{Errors: map[string]error{"zypper needs-rebooting": &host.ExitError{Code: 102}}, Responses: map[string]string{"zypper needs-rebooting": ""}}, Family: "zypper"}
	if got, err := zypper.RebootRequired(context.Background()); err != nil || !got {
		t.Fatalf("zypper exit 102: got %v err %v", got, err)
	}
	broken := Manager{Exec: &host.FakeExec{Errors: map[string]error{"needs-restarting -r": &host.ExitError{Code: 3}}, Responses: map[string]string{"needs-restarting -r": ""}}, Family: "dnf"}
	if _, err := broken.RebootRequired(context.Background()); err == nil {
		t.Fatal("unexpected exit code must be an error")
	}
}

func TestTouchSentinel(t *testing.T) {
	root := t.TempDir()
	if err := TouchSentinel(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "var", "run", "reboot-required")); err != nil {
		t.Fatal(err)
	}
	if err := TouchSentinel(root); err != nil {
		t.Fatal("second touch must succeed")
	}
}
