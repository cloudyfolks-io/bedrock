package network

import (
	"encoding/json"
	"strings"
)

const (
	defaultNetworkAnnotation = "v1.multus-cni.io/default-network"
	NetworkClassWorkload     = "workload"
	NetworkClassManaged      = "managed"
	NetworkClassLabel        = "network.cloudyfolks.com/class"
)

type PodMetadata struct {
	Annotations map[string]string
	Labels      map[string]string
}

func PodAttachment(nic NIC) PodMetadata {
	annotations, labels := PodNetworkMetadata([]NIC{nic})
	if nic.Class != "" {
		primary := TenantWorkloadSubnet
		if nic.Class == NetworkClassManaged {
			primary = TenantManagedSubnet
		}
		prefix := vpcAttachmentProvider + providerAnnotationDomain
		routes, _ := json.Marshal(nic.ManagementRoutes)
		tenant := map[string]string{
			"k8s.v1.cni.cncf.io/networks":          VPCAttachmentNamespace + "/" + VPCAttachmentName,
			"fabric.cloudyfolks.io/logical_switch": primary,
			"fabric.cloudyfolks.io/port_security":  "true",
			"fabric.cloudyfolks.io/default_route":  "false",
			"fabric.cloudyfolks.io/routes":         string(routes),
			prefix + "logical_switch":              FabricName(nic.Namespace, nic.Subnet),
			prefix + "default_route":               "true",
		}
		for key, value := range annotations {
			tenant[prefix+strings.TrimPrefix(key, nicPrefix(nic))] = value
		}
		labels[NetworkClassLabel] = nic.Class
		return PodMetadata{Annotations: tenant, Labels: labels}
	}
	annotations[defaultNetworkAnnotation] = Attachment(nic.Namespace, nic.Subnet)
	return PodMetadata{Annotations: annotations, Labels: labels}
}
