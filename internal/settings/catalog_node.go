package settings

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	KubeletSystemReservedMemory = "kubelet.system-reserved-memory"
	KubeletKubeReservedMemory   = "kubelet.kube-reserved-memory"
	KubeletSystemReservedCPU    = "kubelet.system-reserved-cpu"
	KubeletKubeReservedCPU      = "kubelet.kube-reserved-cpu"
	KubeletEvictionMemory       = "kubelet.eviction-memory-available"
	KubeletEvictionNodefs       = "kubelet.eviction-nodefs-available"
	KubeletEvictionImagefs      = "kubelet.eviction-imagefs-available"
)

func evictionThreshold(v string) error {
	if v == "" {
		return nil
	}
	if percent, ok := strings.CutSuffix(v, "%"); ok {
		quantity, err := resource.ParseQuantity(percent)
		if err != nil || quantity.Sign() <= 0 || quantity.Cmp(resource.MustParse("100")) >= 0 {
			return fmt.Errorf("percentage must be above 0 and below 100, got %q", v)
		}
		return nil
	}
	return positiveQuantity(v)
}

func nodeDefinitions() []Definition {
	return []Definition{
		{KubeletSystemReservedMemory, "2Gi", "Memory the kubelet keeps for the operating system and host daemons on every node. Capped at one eighth of the node memory. Applied on install, join and the K0sReconfigure upgrade step.", positiveQuantity},
		{KubeletKubeReservedMemory, "4Gi", "Memory the kubelet keeps for the k0s processes on every node: etcd, kube-apiserver, kube-controller-manager, kube-scheduler, kubelet and containerd. Capped at one eighth of the node memory. Applied on install, join and the K0sReconfigure upgrade step.", positiveQuantity},
		{KubeletSystemReservedCPU, "500m", "CPU the kubelet keeps for the operating system and host daemons on every node.", positiveQuantity},
		{KubeletKubeReservedCPU, "1", "CPU the kubelet keeps for the k0s processes on every node.", positiveQuantity},
		{KubeletEvictionMemory, "1Gi", "Hard eviction threshold of memory.available, a quantity or a percentage. Capped at one eighth of the node memory when it is a quantity.", evictionThreshold},
		{KubeletEvictionNodefs, "10%", "Hard eviction threshold of nodefs.available, a quantity or a percentage.", evictionThreshold},
		{KubeletEvictionImagefs, "15%", "Hard eviction threshold of imagefs.available, a quantity or a percentage.", evictionThreshold},
	}
}
