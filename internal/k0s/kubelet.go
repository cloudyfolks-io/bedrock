package k0s

import (
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudyfolks-io/bedrock/internal/inventory"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	nodeStatusUpdateFlag  = "--node-status-update-frequency=4s"
	reservedMemoryDivisor = 8
)

type Reservations struct {
	SystemMemory string
	KubeMemory   string
	SystemCPU    string
	KubeCPU      string
	EvictMemory  string
	EvictNodefs  string
	EvictImagefs string
}

var ReservationKeys = []string{
	settings.KubeletSystemReservedMemory,
	settings.KubeletKubeReservedMemory,
	settings.KubeletSystemReservedCPU,
	settings.KubeletKubeReservedCPU,
	settings.KubeletEvictionMemory,
	settings.KubeletEvictionNodefs,
	settings.KubeletEvictionImagefs,
}

func ReservationsFrom(values map[string]string) Reservations {
	get := func(key string) string { return settings.OrDefault(values[key], key) }
	return Reservations{
		SystemMemory: get(settings.KubeletSystemReservedMemory),
		KubeMemory:   get(settings.KubeletKubeReservedMemory),
		SystemCPU:    get(settings.KubeletSystemReservedCPU),
		KubeCPU:      get(settings.KubeletKubeReservedCPU),
		EvictMemory:  get(settings.KubeletEvictionMemory),
		EvictNodefs:  get(settings.KubeletEvictionNodefs),
		EvictImagefs: get(settings.KubeletEvictionImagefs),
	}
}

func cappedQuantity(value string, nodeMemory int64) string {
	if nodeMemory <= 0 {
		return value
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return value
	}
	limit := nodeMemory / reservedMemoryDivisor
	if quantity.Value() <= limit {
		return value
	}
	return resource.NewQuantity(limit, resource.BinarySI).String()
}

func capEviction(value string, nodeMemory int64) string {
	if strings.HasSuffix(value, "%") {
		return value
	}
	return cappedQuantity(value, nodeMemory)
}

func KubeletArgs(r Reservations, nodeMemory int64) []string {
	eviction := strings.Join([]string{
		"memory.available<" + capEviction(r.EvictMemory, nodeMemory),
		"nodefs.available<" + r.EvictNodefs,
		"nodefs.inodesFree<5%",
		"imagefs.available<" + r.EvictImagefs,
	}, ",")
	return []string{
		nodeStatusUpdateFlag,
		"--system-reserved=cpu=" + r.SystemCPU + ",memory=" + cappedQuantity(r.SystemMemory, nodeMemory),
		"--kube-reserved=cpu=" + r.KubeCPU + ",memory=" + cappedQuantity(r.KubeMemory, nodeMemory),
		"--eviction-hard=" + eviction,
	}
}

func NodeMemory(root string) int64 {
	text, err := os.ReadFile(filepath.Join(root, "proc", "meminfo"))
	if err != nil {
		return 0
	}
	bytes, err := inventory.ParseMeminfo(string(text))
	if err != nil {
		return 0
	}
	return bytes
}

func InstallKubeletArgs(root string) []string {
	return KubeletArgs(ReservationsFrom(nil), NodeMemory(root))
}

func UnitEscape(arg string) string {
	return strings.ReplaceAll(arg, "%", "%%")
}
