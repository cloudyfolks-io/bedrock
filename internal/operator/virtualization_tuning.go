package operator

import (
	"fmt"
	"math"
	"strconv"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	keyOvercommitCPU    = "overcommit.cpu"
	keyOvercommitMemory = "overcommit.memory"
	keyMigrationBatch   = "migration.parallel"
	minMemoryOvercommit = 0.1
)

type kubeVirtTuning struct {
	CPUAllocationRatio int64
	MemoryOvercommit   int64
	ParallelMigrations int64
}

func parseTuning(values map[string]string) (kubeVirtTuning, error) {
	cpu, err := positiveInt(values, keyOvercommitCPU)
	if err != nil {
		return kubeVirtTuning{}, err
	}
	memory, err := memoryPercent(values)
	if err != nil {
		return kubeVirtTuning{}, err
	}
	parallel, err := positiveInt(values, keyMigrationBatch)
	if err != nil {
		return kubeVirtTuning{}, err
	}
	return kubeVirtTuning{CPUAllocationRatio: cpu, MemoryOvercommit: memory, ParallelMigrations: parallel}, nil
}

func positiveInt(values map[string]string, key string) (int64, error) {
	parsed, err := strconv.ParseInt(settings.OrDefault(values[key], key), 10, 64)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s: must be a whole number of at least 1", key)
	}
	return parsed, nil
}

func memoryPercent(values map[string]string) (int64, error) {
	parsed, err := strconv.ParseFloat(settings.OrDefault(values[keyOvercommitMemory], keyOvercommitMemory), 64)
	if err != nil || math.IsInf(parsed, 0) || !(parsed >= minMemoryOvercommit) {
		return 0, fmt.Errorf("%s: must be a number of at least %v", keyOvercommitMemory, minMemoryOvercommit)
	}
	return int64(math.Round(parsed * 100)), nil
}
