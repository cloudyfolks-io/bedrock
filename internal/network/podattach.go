package network

const defaultNetworkAnnotation = "v1.multus-cni.io/default-network"

type PodMetadata struct {
	Annotations map[string]string
	Labels      map[string]string
}

func PodAttachment(nic NIC) PodMetadata {
	annotations, labels := PodNetworkMetadata([]NIC{nic})
	annotations[defaultNetworkAnnotation] = Attachment(nic.Namespace, nic.Subnet)
	return PodMetadata{Annotations: annotations, Labels: labels}
}
