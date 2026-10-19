package database

import (
	"context"
	"net/netip"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	keyEnabled          = "database.enabled"
	keyCapacityCPU      = "database.capacity.cpu"
	keyCapacityMemory   = "database.capacity.memory"
	keyCapacityPods     = "database.capacity.pods"
	keyMaxUtilization   = "database.max-utilization"
	keySweeperDryRun    = "database.sweeper-dry-run"
	keyProvisionTimeout = "database.provision-timeout"
	keyExecTimeout      = "database.exec-timeout"
	keyBackupBucket     = "database.backup.bucket"
	keyVIPRange         = "database.vip-range"
	keyRetentionDays    = "database.backup.retention-days"
	keyRetentionDelete  = "database.backup.retention-after-delete"
	settingPrefix       = "database."
)

var configKeys = []string{keyEnabled, keyCapacityCPU, keyCapacityMemory, keyCapacityPods, keyMaxUtilization, keySweeperDryRun, keyProvisionTimeout, keyExecTimeout, keyBackupBucket, keyVIPRange, keyRetentionDays, keyRetentionDelete}

type Config struct {
	Enabled              bool
	CapacityCPU          resource.Quantity
	CapacityMemory       resource.Quantity
	CapacityPods         int
	MaxUtilization       float64
	SweeperDryRun        bool
	ProvisionTimeout     time.Duration
	ExecTimeout          time.Duration
	BackupBucket         string
	VIPRange             netip.Prefix
	RetentionDays        int
	RetentionAfterDelete time.Duration
}

func ParseConfig(values map[string]string) (Config, error) {
	effective := make(map[string]string, len(configKeys))
	for _, key := range configKeys {
		def, _ := settings.Lookup(key)
		value := values[key]
		if value == "" {
			value = def.Default
		}
		if err := def.Validate(value); err != nil {
			return Config{}, err
		}
		effective[key] = value
	}
	enabled, _ := strconv.ParseBool(effective[keyEnabled])
	pods, _ := strconv.Atoi(effective[keyCapacityPods])
	utilization, _ := strconv.ParseFloat(effective[keyMaxUtilization], 64)
	dryRun, _ := strconv.ParseBool(effective[keySweeperDryRun])
	timeout, _ := time.ParseDuration(effective[keyProvisionTimeout])
	execTimeout, _ := time.ParseDuration(effective[keyExecTimeout])
	retentionDays, _ := strconv.Atoi(effective[keyRetentionDays])
	vipRange, _ := netip.ParsePrefix(effective[keyVIPRange])
	retentionAfterDelete, _ := time.ParseDuration(effective[keyRetentionDelete])
	return Config{
		Enabled:              enabled,
		CapacityCPU:          resource.MustParse(effective[keyCapacityCPU]),
		CapacityMemory:       resource.MustParse(effective[keyCapacityMemory]),
		CapacityPods:         pods,
		MaxUtilization:       utilization,
		SweeperDryRun:        dryRun,
		ProvisionTimeout:     timeout,
		ExecTimeout:          execTimeout,
		BackupBucket:         effective[keyBackupBucket],
		VIPRange:             vipRange,
		RetentionDays:        retentionDays,
		RetentionAfterDelete: retentionAfterDelete,
	}, nil
}

func settingValues(ctx context.Context, c client.Reader) (map[string]string, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(list.Items))
	for _, setting := range list.Items {
		values[setting.Name] = setting.Spec.Value
	}
	return values, nil
}

func LoadConfig(ctx context.Context, c client.Reader) (Config, error) {
	values, err := settingValues(ctx, c)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(values)
}
