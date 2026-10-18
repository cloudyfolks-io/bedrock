package network

import (
	"slices"
	"strings"
)

const (
	providerAnnotationDomain = ".cloudyfolks.io/"
	addressAnnotation        = "ip_address"
)

type NIC struct {
	Namespace      string
	Subnet         string
	MAC            string
	Address        string
	SecurityGroups []string
}

func nicPrefix(nic NIC) string {
	return SubnetProvider(nic.Namespace, nic.Subnet) + providerAnnotationDomain
}

func IsAddressAnnotation(key string) bool {
	return strings.HasSuffix(key, providerAnnotationDomain+addressAnnotation)
}

func securityGroupNames(nic NIC) string {
	if len(nic.SecurityGroups) == 0 {
		return DefaultSecurityGroupName(nic.Namespace)
	}
	names := make([]string, len(nic.SecurityGroups))
	for i, group := range nic.SecurityGroups {
		names[i] = FabricName(nic.Namespace, group)
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func nicAnnotations(index int, nic NIC) map[string]string {
	prefix := nicPrefix(nic)
	annotations := map[string]string{prefix + "port_security": "true"}
	annotations[prefix+"security_groups"] = securityGroupNames(nic)
	if nic.MAC != "" {
		annotations[prefix+"mac_address"] = nic.MAC
	}
	if nic.Address != "" {
		annotations[prefix+addressAnnotation] = nic.Address
	}
	if index > 0 {
		annotations[prefix+"default_route"] = "false"
	}
	return annotations
}

func PodNetworkMetadata(nics []NIC) (map[string]string, map[string]string) {
	annotations := map[string]string{}
	labels := map[string]string{}
	for i, nic := range nics {
		for k, v := range nicAnnotations(i, nic) {
			annotations[k] = v
		}
		for _, group := range nic.SecurityGroups {
			labels[SecurityGroupLabel(group)] = "true"
		}
	}
	return annotations, labels
}

func Attachments(nics []NIC) []string {
	out := make([]string, len(nics))
	for i, nic := range nics {
		out[i] = Attachment(nic.Namespace, nic.Subnet)
	}
	return out
}
