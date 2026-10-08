package compute

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func readySubnet(status metav1.ConditionStatus) v1alpha1.Subnet {
	subnet := v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-web", Name: "default"}}
	subnet.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: status}}
	return subnet
}

func TestSubnetReady(t *testing.T) {
	if err := SubnetReady(readySubnet(metav1.ConditionTrue)); err != nil {
		t.Fatal(err)
	}
	if err := SubnetReady(readySubnet(metav1.ConditionFalse)); err != ErrSubnetNotReady {
		t.Fatal(err)
	}
	if err := SubnetReady(v1alpha1.Subnet{}); err != ErrSubnetNotReady {
		t.Fatal(err)
	}
}
