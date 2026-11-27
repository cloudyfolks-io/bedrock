package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildsDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "builds.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBuildDocsDescribeEveryReasonWithCauseAndAction(t *testing.T) {
	for _, reason := range BuildReasons() {
		prefix := "| `" + reason + "` | "
		var row string
		for _, line := range strings.Split(buildsDoc(t), "\n") {
			if strings.HasPrefix(line, prefix) {
				row = line
			}
		}
		if row == "" {
			t.Errorf("docs/builds.md has no table row for the reason %s", reason)
			continue
		}
		if cells := strings.Split(strings.Trim(row, "| "), " | "); len(cells) != 3 || strings.TrimSpace(cells[1]) == "" || strings.TrimSpace(cells[2]) == "" {
			t.Errorf("the row of %s needs a reason, a cause and an action: %q", reason, row)
		}
	}
}

func TestBuildDocsNameTheSubresourceTheReasonsAndTheKataRule(t *testing.T) {
	text := buildsDoc(t)
	for _, name := range []string{"builds/logs", ReasonLogsExpired, "build.job-ttl", "`" + BuildRuntimeClass + "`", "KataUnavailable"} {
		if !strings.Contains(text, name) {
			t.Errorf("docs/builds.md does not mention %s", name)
		}
	}
}
