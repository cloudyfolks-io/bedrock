package compute

import (
	"errors"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

var ErrSubnetNotReady = errors.New("the Subnet is not ready")

type NIC = network.NIC

func SubnetReady(subnet v1alpha1.Subnet) error {
	if !meta.IsStatusConditionPresentAndEqual(subnet.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue) {
		return ErrSubnetNotReady
	}
	return nil
}
