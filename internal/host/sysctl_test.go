package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEnsureBaseSysctlsWritesAndLoadsTheLimits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "etc", "sysctl.d", "80-bedrock-base.conf")
	exec := &FakeExec{Responses: map[string]string{"sysctl -p " + path: ""}}
	if err := EnsureBaseSysctls(context.Background(), exec, root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"fs.inotify.max_user_instances = 8192", "fs.inotify.max_user_watches = 1048576", "vm.max_map_count = 262144"} {
		if !strings.Contains(string(got), line+"\n") {
			t.Fatalf("80-bedrock-base.conf misses %q: %q", line, got)
		}
	}
	if !slices.Contains(exec.Calls, "sysctl -p "+path) {
		t.Fatalf("sysctl -p not run, calls %v", exec.Calls)
	}
}
