package compute

import (
	"errors"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var ErrSubnetNotReady = errors.New("the Subnet is not ready")

type NIC struct {
	Namespace      string
	Subnet         string
	MAC            string
	SecurityGroups []string
}

func SubnetReady(subnet v1alpha1.Subnet) error {
	if !meta.IsStatusConditionPresentAndEqual(subnet.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue) {
		return ErrSubnetNotReady
	}
	return nil
}

func Attachments(nics []NIC) []string {
	out := make([]string, 0, len(nics))
	for _, nic := range nics {
		out = append(out, nic.Namespace+"/"+nic.Subnet)
	}
	return out
}

func PodNetworkMetadata(nics []NIC) (map[string]string, map[string]string) {
	annotations := map[string]string{}
	labels := map[string]string{}
	for index, nic := range nics {
		prefix := AttachmentProvider(nic.Namespace, nic.Subnet) + ".cloudyfolks.io/"
		annotations[prefix+"port_security"] = "true"
		annotations[prefix+"security_groups"] = fabricGroups(nic)
		if nic.MAC != "" {
			annotations[prefix+"mac_address"] = nic.MAC
		}
		if index > 0 {
			annotations[prefix+"default_route"] = "false"
		}
		for _, group := range nic.SecurityGroups {
			labels[LabelSecurityGroup+group] = "true"
		}
	}
	return annotations, labels
}

func fabricGroups(nic NIC) string {
	if len(nic.SecurityGroups) == 0 {
		return DefaultFabricGroup(nic.Namespace)
	}
	names := make([]string, 0, len(nic.SecurityGroups))
	for _, group := range nic.SecurityGroups {
		names = append(names, FabricName(nic.Namespace, group))
	}
	sort.Strings(names)
	return strings.Join(slices.Compact(names), ",")
}
