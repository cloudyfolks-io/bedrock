package fabric

import "k8s.io/apimachinery/pkg/runtime/schema"

const (
	Group   = "fabric.cloudyfolks.io"
	Version = "v1"
)

func gvk(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: Group, Version: Version, Kind: kind}
}

var (
	VpcGVK             = gvk("Vpc")
	SubnetGVK          = gvk("Subnet")
	OvnEipGVK          = gvk("OvnEip")
	OvnFipGVK          = gvk("OvnFip")
	OvnSnatRuleGVK     = gvk("OvnSnatRule")
	RouterLBRuleGVK    = gvk("RouterLBRule")
	DNSZoneGVK         = gvk("DNSZone")
	SecurityGroupGVK   = gvk("SecurityGroup")
	ProviderNetworkGVK = gvk("ProviderNetwork")
	VlanGVK            = gvk("Vlan")
	IPGVK              = gvk("IP")
	BgpConfGVK         = gvk("BgpConf")
	VipGVK             = gvk("Vip")
	SwitchLBRuleGVK    = gvk("SwitchLBRule")
	OvnSnatRuleGVK     = gvk("OvnSnatRule")
)
