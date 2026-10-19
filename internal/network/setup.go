package network

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

var requiredKinds = []schema.GroupKind{
	{Group: fabric.Group, Kind: "Vpc"},
	{Group: fabric.Group, Kind: "Subnet"},
	attachmentGVK.GroupKind(),
}

type registrar interface {
	SetupWithManager(ctrl.Manager) error
}

func SetupWithManager(mgr ctrl.Manager) error {
	known := KindsFromMapper(mgr.GetRESTMapper())
	for _, kind := range requiredKinds {
		if !known(kind) {
			ctrl.Log.WithName("network").Info("the network controllers are not started because a required kind is missing", "kind", kind.String())
			return nil
		}
	}
	c := mgr.GetClient()
	for _, r := range []registrar{
		&UnderlayReconciler{Client: c},
		&NetworkReconciler{Client: c, Reader: mgr.GetAPIReader()},
		&NetworkPeeringReconciler{Client: c, Reader: mgr.GetAPIReader()},
		&SubnetReconciler{Client: c},
		&SecurityGroupReconciler{Client: c},
		&DefaultGroupReconciler{Client: c},
		&ElasticIPReconciler{Client: c},
		&LoadBalancerReconciler{Client: c, Kinds: known},
		&InternetGatewayReconciler{Client: c, Kinds: known},
		&DNSZoneReconciler{Client: c, Kinds: known},
	} {
		if err := r.SetupWithManager(mgr); err != nil {
			return err
		}
	}
	return mgr.Add(&Sweeper{Client: c, Reader: mgr.GetAPIReader()})
}
