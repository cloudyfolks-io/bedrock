package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordingRunner struct {
	scripts []string
	output  string
	err     error
}

func (r *recordingRunner) Run(_ context.Context, stdin string) (string, error) {
	r.scripts = append(r.scripts, stdin)
	return r.output, r.err
}

func TestParseStats(t *testing.T) {
	stats, err := ParseStats("1048576\n7\n1.5\n")
	if err != nil || stats.DiskUsedBytes != 1048576 || stats.Connections != 7 || stats.LagSeconds == nil || *stats.LagSeconds != 1.5 {
		t.Fatalf("stats %+v %v", stats, err)
	}
}

func TestParseStatsRefusesAShortOrBrokenOutput(t *testing.T) {
	for _, out := range []string{"", "1\n2\n", "abc\n7\n0\n", "1\nx\n0\n", "1\n2\nx\n"} {
		if _, err := ParseStats(out); err == nil {
			t.Errorf("output %q accepted", out)
		}
	}
}

func TestReadStatsSetsATimeoutAndRunsTheThreeQueries(t *testing.T) {
	runner := &recordingRunner{output: "2048\n4\n0\n"}
	stats, err := ReadStats(context.Background(), runner)
	if err != nil || stats.DiskUsedBytes != 2048 || stats.Connections != 4 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	script := runner.scripts[0]
	if !strings.HasPrefix(script, "SET statement_timeout") || strings.Count(script, "SELECT") != 3 || !strings.Contains(script, "pg_stat_replication") {
		t.Fatalf("script %q", script)
	}
	if _, err := ReadStats(context.Background(), &recordingRunner{err: errors.New("boom")}); err == nil {
		t.Fatal("runner error hidden")
	}
}
