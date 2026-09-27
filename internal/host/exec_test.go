package host

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRealExecReportsTheExitStatus(t *testing.T) {
	_, err := RealExec{}.Run(context.Background(), "sh", "-c", "echo broken >&2; exit 3")
	if err == nil || err.Error() != "sh -c echo broken >&2; exit 3: exit status 3: broken" {
		t.Fatalf("error %v", err)
	}
}

func TestRealExecReportsWhyTheContextEnded(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, errors.New("timed out after 50ms"))
	defer cancel()
	_, err := RealExec{}.Run(ctx, "sleep", "10")
	if err == nil || err.Error() != "sleep 10: timed out after 50ms: " {
		t.Fatalf("error %v", err)
	}
}
