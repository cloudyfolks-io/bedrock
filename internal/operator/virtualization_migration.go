package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	keyMigrationOutbound    = "migration.parallel-outbound"
	keyMigrationBandwidth   = "migration.bandwidth"
	keyMigrationCompletion  = "migration.completion-timeout-per-gib"
	keyMigrationProgress    = "migration.progress-timeout"
	keyMigrationCompression = "migration.compression"
	compressionZstd         = "zstd"
	migrationPolicyName     = "bedrock-compression"
)

type migrationTuning struct {
	ParallelOutbound        int64
	Bandwidth               string
	CompletionTimeoutPerGiB int64
	ProgressTimeout         int64
}

func parseMigrationTuning(values map[string]string) (migrationTuning, error) {
	outbound, err := positiveInt(values, keyMigrationOutbound)
	if err != nil {
		return migrationTuning{}, err
	}
	completion, err := positiveInt(values, keyMigrationCompletion)
	if err != nil {
		return migrationTuning{}, err
	}
	progress, err := positiveInt(values, keyMigrationProgress)
	if err != nil {
		return migrationTuning{}, err
	}
	bandwidth, err := positiveQuantity(values, keyMigrationBandwidth)
	if err != nil {
		return migrationTuning{}, err
	}
	return migrationTuning{ParallelOutbound: outbound, Bandwidth: bandwidth, CompletionTimeoutPerGiB: completion, ProgressTimeout: progress}, nil
}

func positiveQuantity(values map[string]string, key string) (string, error) {
	quantity, err := resource.ParseQuantity(settingOrDefault(values, key))
	if err != nil || quantity.Sign() <= 0 {
		return "", fmt.Errorf("%s: must be a quantity greater than zero, for example 1Gi", key)
	}
	return quantity.String(), nil
}

func migrationSettings(tuning migrationTuning, parallel int64) map[string]any {
	return map[string]any{
		"parallelMigrationsPerCluster":      parallel,
		"parallelOutboundMigrationsPerNode": tuning.ParallelOutbound,
		"bandwidthPerMigration":             tuning.Bandwidth,
		"completionTimeoutPerGiB":           tuning.CompletionTimeoutPerGiB,
		"progressTimeout":                   tuning.ProgressTimeout,
	}
}

func compressionPolicy(values map[string]string) (policies, absent []*unstructured.Unstructured, err error) {
	switch compression := settingOrDefault(values, keyMigrationCompression); compression {
	case "":
		return nil, []*unstructured.Unstructured{migrationPolicy(map[string]any{})}, nil
	case compressionZstd:
		spec := map[string]any{"selectors": map[string]any{}, "experimental": map[string]any{"compression": compressionZstd}}
		return []*unstructured.Unstructured{migrationPolicy(spec)}, nil, nil
	default:
		return nil, nil, fmt.Errorf("%s: %q is not supported", keyMigrationCompression, compression)
	}
}

func migrationPolicy(spec map[string]any) *unstructured.Unstructured {
	return object("migrations.kubevirt.io/v1alpha1", "MigrationPolicy", "", migrationPolicyName, spec)
}
