package network

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

type SubnetInput struct {
	Subnet     v1alpha1.Subnet
	Network    v1alpha1.Network
	Tenant     string
	Project    string
	DNSServers []string
}

func DHCPv4Options(dnsServers []string) string {
	if len(dnsServers) == 0 {
		return ""
	}
	return "dns_server=" + strings.Join(dnsServers, ";")
}

func gatewayOf(subnet v1alpha1.Subnet) string {
	if subnet.Spec.Gateway != "" {
		return subnet.Spec.Gateway
	}
	gateway, _ := DefaultGateway(subnet.Spec.CIDR)
	return gateway
}

func DesiredFabricSubnet(in SubnetInput) *unstructured.Unstructured {
	subnet := in.Subnet
	options := ""
	if subnet.Spec.DHCP {
		options = DHCPv4Options(in.DNSServers)
	}
	return fabric.Subnet(
		fabric.Meta{
			Name:        FabricName(subnet.Namespace, subnet.Name),
			Labels:      ObjectLabels("Subnet", subnet.Name, in.Tenant, in.Project),
			Annotations: OwnerAnnotations(&subnet),
		},
		fabric.SubnetSpec{
			Vpc:         VpcName(in.Network.Name),
			CIDR:        subnet.Spec.CIDR,
			Gateway:     gatewayOf(subnet),
			Provider:    SubnetProvider(subnet.Namespace, subnet.Name),
			ExcludeIPs:  subnet.Spec.ExcludeIPs,
			DHCP:        subnet.Spec.DHCP,
			DHCPOptions: options,
		},
	)
}

func DesiredAttachment(in SubnetInput) *unstructured.Unstructured {
	subnet := in.Subnet
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"config": AttachmentConfig(subnet.Namespace, subnet.Name)}}}
	u.SetAPIVersion("k8s.cni.cncf.io/v1")
	u.SetKind("NetworkAttachmentDefinition")
	u.SetNamespace(subnet.Namespace)
	u.SetName(subnet.Name)
	u.SetLabels(ObjectLabels("Subnet", subnet.Name, in.Tenant, in.Project))
	u.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               "Subnet",
		Name:               subnet.Name,
		UID:                subnet.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}})
	return u
}
