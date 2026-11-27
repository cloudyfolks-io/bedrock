package mysql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

var StatsQuery = queryPreamble() + `SELECT coalesce(sum(data_length + index_length), 0) FROM information_schema.tables;
SELECT count(*) FROM performance_schema.processlist;
`

func ParseStats(out string) (engine.Stats, error) {
	lines := engine.Lines(out)
	if len(lines) < 2 {
		return engine.Stats{}, fmt.Errorf("the statistics output has %d lines, want 2", len(lines))
	}
	disk, err := strconv.ParseFloat(lines[0], 64)
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the disk size is not a number: %w", err)
	}
	connections, err := strconv.Atoi(lines[1])
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the connection count is not a number: %w", err)
	}
	return engine.Stats{DiskUsedBytes: int64(disk), Connections: connections}, nil
}

func ReadStats(ctx context.Context, runner engine.Runner) (engine.Stats, error) {
	out, err := runner.Run(ctx, StatsQuery)
	if err != nil {
		return engine.Stats{}, err
	}
	return ParseStats(out)
}
