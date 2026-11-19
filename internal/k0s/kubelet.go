package k0s

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudyfolks-io/bedrock/internal/inventory"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	nodeStatusUpdateFlag  = "--node-status-update-frequency=4s"
	reservedMemoryDivisor = 8
)

type KubeletConfig struct {
	SystemMemory string
	KubeMemory   string
	SystemCPU    string
	KubeCPU      string
	EvictMemory  string
	EvictNodefs  string
	EvictImagefs string
	MaxPods      int64
}

var KubeletSettingKeys = []string{
	settings.KubeletSystemReservedMemory,
	settings.KubeletKubeReservedMemory,
	settings.KubeletSystemReservedCPU,
	settings.KubeletKubeReservedCPU,
	settings.KubeletEvictionMemory,
	settings.KubeletEvictionNodefs,
	settings.KubeletEvictionImagefs,
	settings.KubeletMaxPods,
}

func KubeletConfigFrom(values map[string]string) KubeletConfig {
	get := func(key string) string { return settings.OrDefault(values[key], key) }
	maxPods, err := strconv.ParseInt(get(settings.KubeletMaxPods), 10, 32)
	if err != nil {
		maxPods = 0
	}
	return KubeletConfig{
		SystemMemory: get(settings.KubeletSystemReservedMemory),
		KubeMemory:   get(settings.KubeletKubeReservedMemory),
		SystemCPU:    get(settings.KubeletSystemReservedCPU),
		KubeCPU:      get(settings.KubeletKubeReservedCPU),
		EvictMemory:  get(settings.KubeletEvictionMemory),
		EvictNodefs:  get(settings.KubeletEvictionNodefs),
		EvictImagefs: get(settings.KubeletEvictionImagefs),
		MaxPods:      maxPods,
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

func KubeletArgs(r KubeletConfig, nodeMemory int64) []string {
	eviction := strings.Join([]string{
		"memory.available<" + capEviction(r.EvictMemory, nodeMemory),
		"nodefs.available<" + r.EvictNodefs,
		"nodefs.inodesFree<5%",
		"imagefs.available<" + r.EvictImagefs,
	}, ",")
	args := []string{
		nodeStatusUpdateFlag,
		"--system-reserved=cpu=" + r.SystemCPU + ",memory=" + cappedQuantity(r.SystemMemory, nodeMemory),
		"--kube-reserved=cpu=" + r.KubeCPU + ",memory=" + cappedQuantity(r.KubeMemory, nodeMemory),
		"--eviction-hard=" + eviction,
	}
	if r.MaxPods > 0 {
		args = append(args, "--max-pods="+strconv.FormatInt(r.MaxPods, 10))
	}
	return args
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

func InstallKubeletArgs(root string, values map[string]string) []string {
	return KubeletArgs(KubeletConfigFrom(values), NodeMemory(root))
}

func UnitEscape(arg string) string {
	return strings.ReplaceAll(arg, "%", "%%")
}
