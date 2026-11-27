package valkey

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const StatsCommand = "INFO persistence\nINFO clients\n"

func ParseInfo(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			return value
		}
	}
	return ""
}

func ParseStats(out string) (engine.Stats, error) {
	used, err := strconv.ParseInt(strings.TrimSpace(ParseInfo(out, "aof_current_size")), 10, 64)
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the append-only file size is missing or not a number: %w", err)
	}
	connections, err := strconv.Atoi(strings.TrimSpace(ParseInfo(out, "connected_clients")))
	if err != nil {
		return engine.Stats{}, fmt.Errorf("the client count is missing or not a number: %w", err)
	}
	return engine.Stats{DiskUsedBytes: used, Connections: connections}, nil
}

func ReadStats(ctx context.Context, runner engine.Runner) (engine.Stats, error) {
	out, err := execute(ctx, runner, StatsCommand)
	if err != nil {
		return engine.Stats{}, err
	}
	return ParseStats(out)
}
