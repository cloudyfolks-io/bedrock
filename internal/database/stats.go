package database

import (
	"context"
	"math"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

const StatsInterval = 5 * time.Minute

func ReadStats(ctx context.Context, kind string, runner engine.Runner) (engine.Stats, error) {
	switch kind {
	case v1alpha1.DatabaseEnginePostgreSQL:
		return postgres.ReadStats(ctx, runner)
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.ReadStats(ctx, runner)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.ReadStats(ctx, runner)
	}
	return engine.Stats{}, errUnknownEngine(kind)
}

func SampleStats(ctx context.Context, kind string, runner engine.Runner, previous *v1alpha1.DatabaseStats, now time.Time) *v1alpha1.DatabaseStats {
	stats, err := ReadStats(ctx, kind, runner)
	if err != nil {
		return previous
	}
	sampled := metav1.NewTime(now)
	sample := &v1alpha1.DatabaseStats{
		DiskUsedBytes: stats.DiskUsedBytes,
		Connections:   int32(stats.Connections),
		SampledAt:     &sampled,
	}
	if stats.LagSeconds != nil {
		sample.ReplicationLagSeconds = int32(math.Round(*stats.LagSeconds))
	}
	return sample
}

func StatsRequeueIn(previous *v1alpha1.DatabaseStats, now time.Time) time.Duration {
	if previous == nil || previous.SampledAt == nil {
		return 0
	}
	return max(StatsInterval-now.Sub(previous.SampledAt.Time), 0)
}
