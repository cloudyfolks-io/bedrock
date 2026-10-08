package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostChecksReportKVM(t *testing.T) {
	with := t.TempDir()
	if err := os.MkdirAll(filepath.Join(with, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(with, "dev", "kvm"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := kvmPresent(with); got == nil || !*got {
		t.Errorf("root with dev/kvm: %v", got)
	}
	if got := kvmPresent(t.TempDir()); got == nil || *got {
		t.Errorf("root without dev/kvm: %v", got)
	}
}
