package network

import (
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

type SubnetInput struct {
	Subnet            v1alpha1.Subnet
	Network           v1alpha1.Network
	Tenant            string
	Project           string
	DefaultMTU        int
	DefaultDNSServers []string
}

func DHCPv4Options(mtu int, dnsServers []string) string {
	var options []string
	if mtu > 0 {
		options = append(options, "mtu="+strconv.Itoa(mtu))
	}
	if len(dnsServers) > 0 {
		options = append(options, "dns_server="+strings.Join(dnsServers, ";"))
	}
	return strings.Join(options, ",")
}

func gatewayOf(subnet v1alpha1.Subnet) string {
	if subnet.Spec.Gateway != "" {
		return subnet.Spec.Gateway
	}
	gateway, _ := DefaultGateway(subnet.Spec.CIDR)
	return gateway
}

func mtuOf(subnet v1alpha1.Subnet, fallback int) int {
	if subnet.Spec.MTU != nil {
		return int(*subnet.Spec.MTU)
	}
	return fallback
}

func dnsServersOf(subnet v1alpha1.Subnet, fallback []string) []string {
	if len(subnet.Spec.DNSServers) > 0 {
		return subnet.Spec.DNSServers
	}
	return fallback
}

func DesiredFabricSubnet(in SubnetInput) *unstructured.Unstructured {
	subnet := in.Subnet
	mtu := mtuOf(subnet, in.DefaultMTU)
	options := ""
	if subnet.Spec.DHCP {
		options = DHCPv4Options(mtu, dnsServersOf(subnet, in.DefaultDNSServers))
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
			MTU:         mtu,
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
