package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func TestTickReportsWhenItReportedAndWritesNoMoreOftenThanHalfAnInterval(t *testing.T) {
	createHost(t, "node-reported", false, "")
	start := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	deps := newDeps(&host.FakeExec{}, start)
	deps.Root = t.TempDir()
	deps.Node = "node-reported"
	tick := func(at time.Time) time.Time {
		deps.Now = func() time.Time { return at }
		_ = Tick(context.Background(), k8sClient, deps)
		reported := getHost(t, "node-reported").Status.ReportedAt
		if reported == nil {
			t.Fatal("the report time is missing")
		}
		return reported.Time.UTC()
	}
	if got := tick(start); !got.Equal(start) {
		t.Fatalf("first report %v", got)
	}
	if got := tick(start.Add(10 * time.Minute)); !got.Equal(start) {
		t.Fatalf("a tick inside half an interval must keep the time: %v", got)
	}
	later := start.Add(31 * time.Minute)
	if got := tick(later); !got.Equal(later) {
		t.Fatalf("a tick after half an interval must refresh the time: %v", got)
	}
}
