package database

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type outputRunner struct {
	output string
	err    error
}

func (r outputRunner) Run(context.Context, string) (string, error) { return r.output, r.err }

func sampledStats(at time.Time) *v1alpha1.DatabaseStats {
	sampled := metav1.NewTime(at)
	return &v1alpha1.DatabaseStats{DiskUsedBytes: 100, Connections: 2, SampledAt: &sampled}
}

func TestSampleStatsPerEngine(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		kind   string
		output string
		disk   int64
		conns  int32
		lag    int32
	}{
		{v1alpha1.DatabaseEnginePostgreSQL, "4096\n5\n2.4\n", 4096, 5, 2},
		{v1alpha1.DatabaseEngineMySQL, "8192\n6\n", 8192, 6, 0},
		{v1alpha1.DatabaseEngineValkey, "aof_current_size:512\r\nconnected_clients:3\r\n", 512, 3, 0},
	}
	for _, c := range cases {
		got := SampleStats(context.Background(), c.kind, outputRunner{output: c.output}, nil, now)
		if got == nil || got.DiskUsedBytes != c.disk || got.Connections != c.conns || got.ReplicationLagSeconds != c.lag || got.SampledAt == nil || !got.SampledAt.Time.Equal(now) {
			t.Errorf("%s: %+v", c.kind, got)
		}
	}
}

func TestSampleStatsKeepsTheOldValueOnAFailedRead(t *testing.T) {
	then := time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC)
	now := then.Add(time.Hour)
	previous := sampledStats(then)
	for name, runner := range map[string]outputRunner{
		"runner error": {err: errors.New("boom")},
		"short output": {output: "4096\n"},
		"empty output": {},
	} {
		got := SampleStats(context.Background(), v1alpha1.DatabaseEngineMySQL, runner, previous, now)
		if got != previous || !got.SampledAt.Time.Equal(then) || got.DiskUsedBytes != 100 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := SampleStats(context.Background(), v1alpha1.DatabaseEngineMySQL, outputRunner{err: errors.New("boom")}, nil, now); got != nil {
		t.Errorf("a failed first read made stats: %+v", got)
	}
	if got := SampleStats(context.Background(), "oracle", outputRunner{output: "1\n2\n"}, previous, now); got != previous {
		t.Errorf("an unknown engine changed the stats: %+v", got)
	}
}

func TestStatsRequeueIsFiveMinutesAfterTheLastSample(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		previous *v1alpha1.DatabaseStats
		want     time.Duration
	}{
		{"never sampled", nil, 0},
		{"sample without time", &v1alpha1.DatabaseStats{}, 0},
		{"just sampled", sampledStats(now), 5 * time.Minute},
		{"two minutes ago", sampledStats(now.Add(-2 * time.Minute)), 3 * time.Minute},
		{"overdue", sampledStats(now.Add(-time.Hour)), 0},
	}
	for _, c := range cases {
		if got := StatsRequeueIn(c.previous, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if StatsInterval != 5*time.Minute {
		t.Fatalf("interval %v", StatsInterval)
	}
}
