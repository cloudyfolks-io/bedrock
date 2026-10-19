package registry

import (
	"strconv"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

type Config struct {
	Host         string
	Aliases      []string
	AdminHost    string
	AdminAllow   []string
	DBInstances  int
	StorageClass string
	Bucket       string
	BackupBucket string
	TLSMode      string
}

func ParseConfig(values map[string]string) (Config, error) {
	effective := map[string]string{}
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
	instances, _ := strconv.Atoi(effective["registry.db-instances"])
	return Config{
		Host:         effective["registry.host"],
		Aliases:      splitList(effective["registry.aliases"]),
		AdminHost:    effective["registry.admin-host"],
		AdminAllow:   splitList(effective["registry.admin-allow-cidrs"]),
		DBInstances:  instances,
		StorageClass: effective["registry.storage-class"],
		Bucket:       effective["registry.bucket"],
		BackupBucket: effective["database.backup.bucket"],
		TLSMode:      values[tlsModeKey],
	}, nil
}

var configKeys = []string{
	"registry.host", "registry.aliases", "registry.admin-host", "registry.admin-allow-cidrs",
	"registry.db-instances", "registry.storage-class", "registry.bucket", "database.backup.bucket",
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func (c Config) PublicHosts() []string {
	return append([]string{c.Host}, c.Aliases...)
}
