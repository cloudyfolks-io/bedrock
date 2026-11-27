package operator

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

type cephSizing struct {
	osd             map[string]any
	osdMemoryTarget string
	mon             map[string]any
	mgr             map[string]any
	mds             map[string]any
	rgw             map[string]any
}

var rgwConfigTuning = map[string]any{
	"rgw_gc_max_concurrent_io":             "20",
	"rgw_override_bucket_index_max_shards": "101",
	"rgw_bucket_quota_ttl":                 "60",
	"rgw_user_quota_bucket_sync_interval":  "60",
	"rgw_user_quota_sync_interval":         "60",
}

func quantity(values map[string]string, key string) (resource.Quantity, error) {
	q, err := resource.ParseQuantity(settings.OrDefault(values[key], key))
	if err != nil {
		return q, fmt.Errorf("%s: %w", key, err)
	}
	return q, nil
}

func scaled(q resource.Quantity, numerator, denominator int64) resource.Quantity {
	return *resource.NewQuantity(q.Value()*numerator/denominator, resource.BinarySI)
}

func resourceList(cpu string, memory resource.Quantity) map[string]any {
	list := map[string]any{"memory": memory.String()}
	if cpu != "" {
		list["cpu"] = cpu
	}
	return list
}

func burstable(requestCPU, limitCPU string, request, limit resource.Quantity) map[string]any {
	return map[string]any{"requests": resourceList(requestCPU, request), "limits": resourceList(limitCPU, limit)}
}

func cephSizingFrom(values map[string]string) (cephSizing, error) {
	var sizing cephSizing
	memory := map[string]resource.Quantity{}
	for _, key := range []string{settings.StorageOSDMemory, settings.StorageOSDMemoryTarget, settings.StorageMonMemory, settings.StorageMgrMemory, settings.StorageMDSMemory, settings.StorageRGWMemory} {
		q, err := quantity(values, key)
		if err != nil {
			return sizing, err
		}
		memory[key] = q
	}
	cpu := settings.OrDefault(values[settings.StorageOSDCPU], settings.StorageOSDCPU)
	if _, err := resource.ParseQuantity(cpu); err != nil {
		return sizing, fmt.Errorf("%s: %w", settings.StorageOSDCPU, err)
	}
	osd, target := memory[settings.StorageOSDMemory], memory[settings.StorageOSDMemoryTarget]
	if target.Cmp(osd) >= 0 {
		return sizing, fmt.Errorf("%s must be lower than %s", settings.StorageOSDMemoryTarget, settings.StorageOSDMemory)
	}
	sizing.osd = burstable(cpu, cpu, osd, osd)
	sizing.osdMemoryTarget = strconv.FormatInt(target.Value(), 10)
	mon, mgr, mds, rgw := memory[settings.StorageMonMemory], memory[settings.StorageMgrMemory], memory[settings.StorageMDSMemory], memory[settings.StorageRGWMemory]
	sizing.mon = burstable("500m", "", mon, scaled(mon, 3, 2))
	sizing.mgr = burstable("500m", "", mgr, scaled(mgr, 2, 1))
	sizing.mds = burstable("500m", "", mds, scaled(mds, 2, 1))
	sizing.rgw = burstable("1", "2", rgw, scaled(rgw, 2, 1))
	return sizing, nil
}

func (s cephSizing) cephConfig() map[string]any {
	return map[string]any{
		"osd": map[string]any{"osd_memory_target": s.osdMemoryTarget},
		"global": map[string]any{
			"rbd_blocklist_on_break_lock":  "false",
			"rbd_blocklist_expire_seconds": "60",
		},
		"mon": map[string]any{"mon_osd_blocklist_default_expire": "60"},
	}
}
