package network

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const servicesLoadBalancerPoolName = "services-plane"

func ServicesLoadBalancerPool(cfg Config) *unstructured.Unstructured {
	subnet, configured := ServicesSubnet(cfg)
	if !configured {
		return nil
	}
	pool := newObject(fabric.LoadBalancerPoolGVK, servicesLoadBalancerPoolName)
	pool.SetLabels(underlayMeta(pool.GetKind(), pool.GetName()).Labels)
	pool.Object["spec"] = map[string]any{"subnet": subnet, "announce": "l2", "default": false}
	return pool
}

func (r *UnderlayReconciler) syncServicesPool(ctx context.Context, cfg Config) (bool, error) {
	pool := ServicesLoadBalancerPool(cfg)
	if pool == nil {
		return true, nil
	}
	existing, err := getObject(ctx, r.Client, fabric.LoadBalancerPoolGVK, pool.GetName())
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Client.Create(ctx, pool, client.FieldOwner(v1alpha1.NetworkFieldManager)); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	case existing.GetLabels()[LabelComponent] != ComponentUnderlay || existing.GetLabels()[LabelManaged] != "true":
		return false, fmt.Errorf("the LoadBalancerPool %s belongs to another owner", pool.GetName())
	case !reflect.DeepEqual(existing.Object["spec"], pool.Object["spec"]):
		return false, fmt.Errorf("the LoadBalancerPool %s has different settings; drain its Services before changing or replacing it", pool.GetName())
	}
	vpc, err := getObject(ctx, r.Client, fabric.VpcGVK, tenantManagementVPC)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	before := vpc.DeepCopy()
	subnet := nestedStringOf(pool, "spec", "subnet")
	external := fabric.VpcSpecExternalSubnets(vpc)
	if !slices.Contains(external, subnet) {
		external = append(external, subnet)
	}
	if err := unstructured.SetNestedStringSlice(vpc.Object, external, "spec", "extraExternalSubnets"); err != nil {
		return false, err
	}
	if err := unstructured.SetNestedField(vpc.Object, true, "spec", "enableExternal"); err != nil {
		return false, err
	}
	if reflect.DeepEqual(vpc.Object, before.Object) {
		return true, nil
	}
	return true, r.Client.Patch(ctx, vpc, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.NetworkFieldManager))
}
