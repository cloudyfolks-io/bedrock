package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	statsTimeoutMillis = 30000

	StatsQuery = `SELECT coalesce(sum(pg_database_size(datname)), 0) FROM pg_database;
SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend';
SELECT coalesce(max(extract(epoch FROM replay_lag)), 0) FROM pg_stat_replication;
`
)

func statsScript() string {
	return fmt.Sprintf("SET statement_timeout = '%d';\nSET idle_in_transaction_session_timeout = '%d';\n", statsTimeoutMillis, statsTimeoutMillis) + StatsQuery
}

func ParseStats(out string) (engine.Stats, error) {
	lines := engine.Lines(out)
	if len(lines) < 3 {
		return engine.Stats{}, fmt.Errorf("the statistics output has %d lines, want 3", len(lines))
	}
	disk, err := strconv.ParseInt(lines[0], 10, 64)
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the disk size is not a number: %w", err)
	}
	connections, err := strconv.Atoi(lines[1])
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the connection count is not a number: %w", err)
	}
	lag, err := strconv.ParseFloat(lines[2], 64)
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the replication lag is not a number: %w", err)
	}
	return engine.Stats{DiskUsedBytes: disk, Connections: connections, LagSeconds: &lag}, nil
}

func ReadStats(ctx context.Context, runner engine.Runner) (engine.Stats, error) {
	out, err := runner.Run(ctx, statsScript())
	if err != nil {
		return engine.Stats{}, err
	}
	return ParseStats(out)
}

func Target(name string) engine.Target {
	return engine.Target{
		Selector:  "cnpg.io/cluster=" + name + ",cnpg.io/instanceRole=primary",
		Container: "postgres",
	}
}
