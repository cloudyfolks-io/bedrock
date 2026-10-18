package network

import (
	"cmp"
	"slices"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type PoolUsage struct {
	Name      string
	Using     int64
	Available int64
}

func usedShare(p PoolUsage) float64 {
	return float64(p.Using) / float64(p.Using+p.Available)
}

func LeastUsed(pools []PoolUsage) (string, bool) {
	open := slices.DeleteFunc(slices.Clone(pools), func(p PoolUsage) bool { return p.Available <= 0 })
	if len(open) == 0 {
		return "", false
	}
	best := slices.MinFunc(open, func(a, b PoolUsage) int {
		return cmp.Or(cmp.Compare(usedShare(a), usedShare(b)), cmp.Compare(a.Name, b.Name))
	})
	return best.Name, true
}

func IsTenantPool(pn v1alpha1.ProviderNetworkSpec) bool {
	return pn.Role == "" || pn.Role == v1alpha1.ProviderRoleTenant
}

func TenantPoolNames(cfg Config) []string {
	var names []string
	for _, pn := range cfg.ProviderNetworks {
		carriesRouterPorts := cfg.EIPMode == EIPModeBGP && pn.Name == cfg.BGP.Transit
		if IsTenantPool(pn) && !carriesRouterPorts {
			names = append(names, pn.Name)
		}
	}
	return names
}

func Usability(role string) string {
	switch role {
	case v1alpha1.ProviderRolePlatform:
		return "private"
	case v1alpha1.ProviderRoleTransit:
		return "transit"
	case v1alpha1.ProviderRoleServices:
		return "services"
	}
	return "public"
}

func PoolOf(eip v1alpha1.ElasticIP) string {
	return cmp.Or(eip.Spec.ProviderNetwork, eip.Status.ProviderNetwork)
}
