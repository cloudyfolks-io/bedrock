package network

import (
	"context"
	"fmt"
	"net/netip"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

var planeKinds = []schema.GroupVersionKind{fabric.OvnSnatRuleGVK, fabric.OvnEipGVK}

func (r *NetworkReconciler) attachServices(ctx context.Context, network *v1alpha1.Network, cfg Config, external External) (*Violation, error) {
	if !network.Spec.EnableExternal {
		return nil, r.dropServicesIdentity(ctx, network)
	}
	if _, configured := ServicesSubnet(cfg); !configured || external.Reason != ReasonConnected {
		return nil, nil
	}
	identity, refusal, err := r.servicesIdentity(ctx, network, cfg)
	if err != nil || refusal != nil {
		return refusal, err
	}
	plane := ServicesPlane(*network, cfg, identity)
	for _, object := range plane.Guarded() {
		conflict, err := r.conflicts(ctx, object, network)
		if err != nil {
			return nil, err
		}
		if conflict {
			return &Violation{Reason: ReasonFabricConflict, Message: fmt.Sprintf("a Fabric %s named %s belongs to another owner", object.GetKind(), object.GetName())}, nil
		}
	}
	for _, object := range plane.All() {
		if err := ssa.Apply(ctx, r.Client, object, v1alpha1.NetworkFieldManager); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (r *NetworkReconciler) servicesIdentity(ctx context.Context, network *v1alpha1.Network, cfg Config) (string, *Violation, error) {
	if recorded := network.Status.ServicesIdentity; recorded != "" {
		return recorded, nil, nil
	}
	used, err := r.usedIdentities(ctx, network, cfg)
	if err != nil {
		return "", nil, err
	}
	identity, err := r.ownedIdentity(ctx, network, cfg, used)
	if err != nil {
		return "", nil, err
	}
	if identity == "" {
		next, nextErr := NextServicesIdentity(cfg.Services.IdentityRange, used)
		if nextErr != nil {
			return "", &Violation{Reason: ReasonNoPoolCapacity, Message: nextErr.Error()}, nil
		}
		identity = next
	}
	recorded, err := r.recordIdentity(ctx, network, identity)
	return recorded, nil, err
}

func (r *NetworkReconciler) usedIdentities(ctx context.Context, network *v1alpha1.Network, cfg Config) ([]string, error) {
	var networks v1alpha1.NetworkList
	if err := r.live().List(ctx, &networks); err != nil {
		return nil, err
	}
	var used []string
	for _, other := range networks.Items {
		if other.Name != network.Name && other.Status.ServicesIdentity != "" {
			used = append(used, other.Status.ServicesIdentity)
		}
	}
	pool, _ := ProviderOf(cfg, cfg.Services.ProviderNetwork)
	used = append(used, pool.Gateway)
	eips, err := listObjects(ctx, r.live(), fabric.OvnEipGVK)
	if err != nil {
		return nil, err
	}
	own, subnet := ServicesIdentityName(VpcName(network.Name)), ExternalSubnetName(pool.Name)
	for _, eip := range eips {
		if eip.GetName() == own || specText(eip, "externalSubnet") != subnet {
			continue
		}
		used = append(used, specText(eip, "v4Ip"), fabric.EipAddress(eip))
	}
	return used, nil
}

func (r *NetworkReconciler) ownedIdentity(ctx context.Context, network *v1alpha1.Network, cfg Config, used []string) (string, error) {
	eip, err := getObject(ctx, r.live(), fabric.OvnEipGVK, ServicesIdentityName(VpcName(network.Name)))
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}
	if err != nil || !ownedBy(eip, network) {
		return "", nil
	}
	address, parseErr := netip.ParseAddr(specText(eip, "v4Ip"))
	prefix, _ := netip.ParsePrefix(cfg.Services.IdentityRange)
	if parseErr != nil || !prefix.Contains(address) || containsString(used, address.String()) {
		return "", nil
	}
	return address.String(), nil
}

func (r *NetworkReconciler) recordIdentity(ctx context.Context, network *v1alpha1.Network, identity string) (string, error) {
	claimed := identity
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.live().Get(ctx, client.ObjectKeyFromObject(network), network); err != nil {
			return err
		}
		if recorded := network.Status.ServicesIdentity; recorded != "" {
			claimed = recorded
			return nil
		}
		before := network.DeepCopy()
		network.Status.ServicesIdentity = identity
		return r.Client.Status().Patch(ctx, network, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.NetworkFieldManager))
	})
	return claimed, err
}

func specText(u *unstructured.Unstructured, field string) string {
	value, _, _ := unstructured.NestedString(u.Object, "spec", field)
	return value
}

func (r *NetworkReconciler) ownedPlane(ctx context.Context, network *v1alpha1.Network) ([]*unstructured.Unstructured, error) {
	selector := client.MatchingLabels{v1alpha1.LabelKind: "Network", v1alpha1.LabelName: network.Name}
	var owned []*unstructured.Unstructured
	for _, gvk := range planeKinds {
		items, err := listObjects(ctx, r.Client, gvk, selector)
		if meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if ownedBy(item, network) {
				owned = append(owned, item)
			}
		}
	}
	return owned, nil
}

func (r *NetworkReconciler) dropServicesIdentity(ctx context.Context, network *v1alpha1.Network) error {
	owned, err := r.ownedPlane(ctx, network)
	if err != nil {
		return err
	}
	for _, object := range owned {
		if specText(object, "type") == "lrp" {
			continue
		}
		if err := deleteObject(ctx, r.Client, object.GroupVersionKind(), object.GetName()); err != nil {
			return err
		}
	}
	return nil
}

func (r *NetworkReconciler) deleteServicesPlane(ctx context.Context, network *v1alpha1.Network) (bool, error) {
	owned, err := r.ownedPlane(ctx, network)
	if err != nil {
		return false, err
	}
	for _, object := range owned {
		if err := deleteObject(ctx, r.Client, object.GroupVersionKind(), object.GetName()); err != nil {
			return false, err
		}
	}
	remaining, err := r.ownedPlane(ctx, network)
	return len(remaining) == 0, err
}
