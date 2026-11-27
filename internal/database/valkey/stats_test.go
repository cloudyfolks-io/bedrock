package valkey

import (
	"context"
	"errors"
	"testing"
)

func TestParseInfo(t *testing.T) {
	out := "# Persistence\r\naof_current_size:2048\r\n# Clients\r\nconnected_clients:7\r\n"
	if got := ParseInfo(out, "aof_current_size"); got != "2048" {
		t.Fatalf("aof %q", got)
	}
	if got := ParseInfo(out, "missing"); got != "" {
		t.Fatalf("missing %q", got)
	}
}

func TestParseStats(t *testing.T) {
	stats, err := ParseStats("# Persistence\r\naof_current_size:2048\r\n# Clients\r\nconnected_clients:7\r\n")
	if err != nil || stats.DiskUsedBytes != 2048 || stats.Connections != 7 || stats.LagSeconds != nil {
		t.Fatalf("stats %+v %v", stats, err)
	}
}

func TestParseStatsRefusesAShortOrBrokenOutput(t *testing.T) {
	for _, out := range []string{"", "# Persistence\r\n", "aof_current_size:2048\r\n", "connected_clients:7\r\n", "aof_current_size:x\r\nconnected_clients:7\r\n"} {
		if _, err := ParseStats(out); err == nil {
			t.Errorf("output %q accepted", out)
		}
	}
}

func TestReadStatsChecksTheReplies(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"aof_current_size:10\r\nconnected_clients:2\r\n"}}
	stats, err := ReadStats(context.Background(), runner)
	if err != nil || stats.DiskUsedBytes != 10 || stats.Connections != 2 || runner.scripts[0] != "INFO persistence\nINFO clients\n" {
		t.Fatalf("stats %+v %v %q", stats, err, runner.scripts)
	}
	if _, err := ReadStats(context.Background(), &fakeRunner{outputs: []string{"NOAUTH Authentication required.\n"}}); err == nil {
		t.Fatal("an auth error was read as stats")
	}
	if _, err := ReadStats(context.Background(), &fakeRunner{err: errors.New("boom")}); err == nil {
		t.Fatal("runner error hidden")
	}
}
