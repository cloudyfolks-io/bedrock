package network

import "slices"

const (
	ReasonVpcReady                       = "VpcReady"
	ReasonVpcPending                     = "VpcPending"
	ReasonFabricConflict                 = "FabricConflict"
	ReasonTerminating                    = "Terminating"
	ReasonConnected                      = "Connected"
	ReasonPending                        = "Pending"
	ReasonNoProviderNetwork              = "NoProviderNetwork"
	ReasonBGPNotConfigured               = "BGPNotConfigured"
	ReasonApplied                        = "Applied"
	ReasonNetworkNotFound                = "NetworkNotFound"
	ReasonNetworkNotOwned                = "NetworkNotOwned"
	ReasonNetworkNotReady                = "NetworkNotReady"
	ReasonCIDROutsideNetwork             = "CIDROutsideNetwork"
	ReasonCIDROverlap                    = "CIDROverlap"
	ReasonGatewayInvalid                 = "GatewayInvalid"
	ReasonExcludeIPsInvalid              = "ExcludeIPsInvalid"
	ReasonNamespaceNotLabeled            = "NamespaceNotLabeled"
	ReasonFabricNotReady                 = "FabricNotReady"
	ReasonInUse                          = "InUse"
	ReasonReserved                       = "Reserved"
	ReasonAttached                       = "Attached"
	ReasonUnknownProviderNetwork         = "UnknownProviderNetwork"
	ReasonUnsupported                    = "Unsupported"
	ReasonAddressOutsideProviderNetwork  = "AddressOutsideProviderNetwork"
	ReasonAddressOutsideSubnet           = "AddressOutsideSubnet"
	ReasonAddressReserved                = "AddressReserved"
	ReasonAddressInUse                   = "AddressInUse"
	ReasonAllocating                     = "Allocating"
	ReasonTargetNotFound                 = "TargetNotFound"
	ReasonTargetMismatch                 = "TargetMismatch"
	ReasonWaitingForAddress              = "WaitingForAddress"
	ReasonNetworkNotExternal             = "NetworkNotExternal"
	ReasonExternalNotConnected           = "ExternalNotConnected"
	ReasonFRRAgentMissing                = "FRRAgentMissing"
	ReasonProgrammed                     = "Programmed"
	ReasonElasticIPNotFound              = "ElasticIPNotFound"
	ReasonElasticIPNotReady              = "ElasticIPNotReady"
	ReasonNoTargets                      = "NoTargets"
	ReasonTargetNetworkMismatch          = "TargetNetworkMismatch"
	ReasonTargetPortsDiffer              = "TargetPortsDiffer"
	ReasonNotAttached                    = "NotAttached"
	ReasonInvalidRecords                 = "InvalidRecords"
	ReasonRefNotFound                    = "RefNotFound"
	ReasonInvalidRules                   = "InvalidRules"
	ReasonTargetInUse                    = "TargetInUse"
	ReasonWaitingForDefaultSecurityGroup = "WaitingForDefaultSecurityGroup"
	ReasonCIDRReserved                   = "CIDRReserved"
	ReasonCIDROutsideAllowedRanges       = "CIDROutsideAllowedRanges"
	ReasonVRFInUse                       = "VRFInUse"
	ReasonVRFReserved                    = "VRFReserved"
	ReasonNoPoolCapacity                 = "NoPoolCapacity"
	ReasonQuotaExceeded                  = "QuotaExceeded"
)

var AllReasons = sortedReasons([]string{
	ReasonVpcReady, ReasonVpcPending, ReasonFabricConflict, ReasonTerminating, ReasonConnected, ReasonPending,
	ReasonNoProviderNetwork, ReasonBGPNotConfigured, ReasonApplied, ReasonNetworkNotFound, ReasonNetworkNotOwned,
	ReasonNetworkNotReady, ReasonCIDROutsideNetwork, ReasonCIDROverlap, ReasonGatewayInvalid, ReasonExcludeIPsInvalid,
	ReasonNamespaceNotLabeled, ReasonFabricNotReady, ReasonInUse, ReasonReserved, ReasonAttached,
	ReasonUnknownProviderNetwork, ReasonUnsupported, ReasonAddressOutsideProviderNetwork, ReasonAddressOutsideSubnet, ReasonAddressReserved,
	ReasonAddressInUse, ReasonAllocating, ReasonTargetNotFound, ReasonTargetMismatch, ReasonWaitingForAddress,
	ReasonNetworkNotExternal, ReasonExternalNotConnected, ReasonFRRAgentMissing, ReasonProgrammed,
	ReasonElasticIPNotFound, ReasonElasticIPNotReady, ReasonNoTargets, ReasonTargetNetworkMismatch,
	ReasonTargetPortsDiffer, ReasonNotAttached, ReasonInvalidRecords, ReasonRefNotFound, ReasonInvalidRules,
	ReasonTargetInUse, ReasonWaitingForDefaultSecurityGroup, ReasonCIDRReserved, ReasonCIDROutsideAllowedRanges,
	ReasonVRFInUse, ReasonVRFReserved, ReasonNoPoolCapacity,
	ReasonQuotaExceeded,
})

func sortedReasons(reasons []string) []string {
	out := slices.Clone(reasons)
	slices.Sort(out)
	return slices.Compact(out)
}
