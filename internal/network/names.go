package network

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	Finalizer                = "network.bedrock.cloudyfolks.io/fabric"
	LabelManaged             = "network.bedrock.cloudyfolks.io/managed"
	LabelComponent           = "network.bedrock.cloudyfolks.io/component"
	ComponentUnderlay        = "underlay"
	LabelUsability           = "usability"
	AnnotationOwnerNamespace = "bedrock.cloudyfolks.io/owner-namespace"
	AnnotationOwnerName      = "bedrock.cloudyfolks.io/owner-name"
	AnnotationOwnerUID       = "bedrock.cloudyfolks.io/owner-uid"
	AnnotationVrfID          = "network.bedrock.cloudyfolks.io/vrf-id"
	ConditionExternalReady   = "ExternalReady"
	ConditionRangePolicy     = "RangePolicy"
	ConditionVRFPolicy       = "VRFPolicy"

	labelValueMax       = 63
	hashLength          = 12
	listenerRuleMax     = 59
	securityGroupPrefix = "sg.bedrock.cloudyfolks.io/"
	providerPrefix      = "provider-"
)

func shorten(key, identity string, max int) string {
	sum := sha256.Sum256([]byte(identity))
	head := sanitize(key)
	room := max - hashLength - 1
	if len(head) > room {
		head = head[:room]
	}
	return strings.TrimRight(head, "-") + "-" + hex.EncodeToString(sum[:])[:hashLength]
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, s)
}

func FabricName(namespace, name string) string {
	return shorten(namespace+"-"+name, namespace+"/"+name, labelValueMax)
}

func VpcName(network string) string {
	return network
}

func ExternalSubnetName(providerNetwork string) string {
	return providerPrefix + providerNetwork
}

func LBSubnetName(network string) string {
	name := network + "-lb"
	if len(name) <= labelValueMax {
		return name
	}
	return shorten(name, network+"/lb", labelValueMax)
}

func InternalVipName(namespace, loadBalancer string) string {
	return shorten(namespace+"-"+loadBalancer+"-vip", strings.Join([]string{namespace, loadBalancer, "vip"}, "/"), labelValueMax)
}

func SubnetProvider(namespace, subnet string) string {
	return subnet + "." + namespace + ".fabric"
}

func Attachment(namespace, subnet string) string {
	return namespace + "/" + subnet
}

func AttachmentConfig(namespace, subnet string) string {
	return `{"cniVersion":"0.3.0","type":"fabric","server_socket":"/run/openvswitch/fabric-daemon.sock","provider":"` + SubnetProvider(namespace, subnet) + `"}`
}

func SecurityGroupLabel(securityGroup string) string {
	return securityGroupPrefix + securityGroup
}

func DefaultSecurityGroupName(namespace string) string {
	return "default-" + namespace
}

func ListenerRuleName(namespace, loadBalancer string, port int32, protocol string) string {
	suffix := strconv.Itoa(int(port)) + "-" + strings.ToLower(protocol)
	return shorten(namespace+"-"+loadBalancer+"-"+suffix, strings.Join([]string{namespace, loadBalancer, suffix}, "/"), listenerRuleMax)
}

func DNSZoneName(namespace, zone, network string) string {
	return shorten(namespace+"-"+zone+"-"+network, strings.Join([]string{namespace, zone, network}, "/"), labelValueMax)
}

func ObjectLabels(kind, name, tenant, project string) map[string]string {
	labels := map[string]string{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name, LabelManaged: "true"}
	if tenant == "" {
		return labels
	}
	for k, v := range v1alpha1.TenantLabels(kind, name, tenant, project) {
		labels[k] = v
	}
	return labels
}

func OwnerAnnotations(owner metav1.Object) map[string]string {
	return map[string]string{
		AnnotationOwnerNamespace: owner.GetNamespace(),
		AnnotationOwnerName:      owner.GetName(),
		AnnotationOwnerUID:       string(owner.GetUID()),
	}
}

func OwnerOf(obj metav1.Object) (types.NamespacedName, types.UID, bool) {
	annotations := obj.GetAnnotations()
	name, uid := annotations[AnnotationOwnerName], annotations[AnnotationOwnerUID]
	if name == "" || uid == "" {
		return types.NamespacedName{}, "", false
	}
	return types.NamespacedName{Namespace: annotations[AnnotationOwnerNamespace], Name: name}, types.UID(uid), true
}
