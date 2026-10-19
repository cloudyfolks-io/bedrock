package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func containersDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "containers.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDocsDescribeEveryReason(t *testing.T) {
	text := containersDoc(t)
	for _, reason := range Reasons() {
		if !strings.Contains(text, "| `"+reason+"` |") {
			t.Errorf("docs/containers.md has no table row for the reason %s", reason)
		}
	}
}

func TestDocsNameTheSettingAndTheSubresources(t *testing.T) {
	text := containersDoc(t)
	for _, name := range []string{RuntimeSetting, "containers/logs", "containers/exec", "containers/restart", "count/containers.bedrock.cloudyfolks.io", RuntimeNone} {
		if !strings.Contains(text, name) {
			t.Errorf("docs/containers.md does not mention %s", name)
		}
	}
}

func TestReasonsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range Reasons() {
		if seen[reason] {
			t.Errorf("the reason %s is listed twice", reason)
		}
		seen[reason] = true
	}
}
