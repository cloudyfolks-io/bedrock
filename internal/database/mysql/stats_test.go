package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseStats(t *testing.T) {
	stats, err := ParseStats("1048576\n7\n")
	if err != nil || stats.DiskUsedBytes != 1048576 || stats.Connections != 7 || stats.LagSeconds != nil {
		t.Fatalf("stats %+v %v", stats, err)
	}
	stats, err = ParseStats("1.048576e6\n3\n")
	if err != nil || stats.DiskUsedBytes != 1048576 || stats.Connections != 3 {
		t.Fatalf("scientific %+v %v", stats, err)
	}
}

func TestParseStatsRefusesAShortOrBrokenOutput(t *testing.T) {
	for _, out := range []string{"", "1048576\n", "abc\n7\n", "1048576\nx\n"} {
		if _, err := ParseStats(out); err == nil {
			t.Errorf("output %q accepted", out)
		}
	}
}

func TestReadStatsRunsOneQueryWithATimeout(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"2048\n4\n"}}
	stats, err := ReadStats(context.Background(), runner)
	if err != nil || stats.DiskUsedBytes != 2048 || stats.Connections != 4 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	if len(runner.scripts) != 1 || !strings.HasPrefix(runner.scripts[0], "SET SESSION max_execution_time") {
		t.Fatalf("scripts %q", runner.scripts)
	}
	if _, err := ReadStats(context.Background(), &fakeRunner{err: errors.New("boom")}); err == nil {
		t.Fatal("runner error hidden")
	}
}
