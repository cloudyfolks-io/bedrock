package operator

import (
	"fmt"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

var smbiosFields = map[string]string{
	"manufacturer": "virt.smbios.manufacturer",
	"product":      "virt.smbios.product",
	"family":       "virt.smbios.family",
	"version":      "virt.smbios.version",
	"sku":          "virt.smbios.sku",
}

func parseSMBIOS(values map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for field, key := range smbiosFields {
		value := values[key]
		if value == "" {
			continue
		}
		definition, _ := settings.Lookup(key)
		if err := definition.Validate(value); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[field] = value
	}
	return out, nil
}
