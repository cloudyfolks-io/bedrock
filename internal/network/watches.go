package network

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func requestOf(namespace, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

func requestsFrom[T any, P interface {
	*T
	client.Object
}](items []T) []reconcile.Request {
	requests := make([]reconcile.Request, len(items))
	for i := range items {
		object := P(&items[i])
		requests[i] = requestOf(object.GetNamespace(), object.GetName())
	}
	return requests
}

func OwnerRequests(obj client.Object) []reconcile.Request {
	key, _, ok := OwnerOf(obj)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: key}}
}

func NetworkOfSubnet(subnet v1alpha1.Subnet) []reconcile.Request {
	return []reconcile.Request{requestOf("", subnet.Spec.NetworkRef)}
}

func SubnetsOfNetwork(subnets []v1alpha1.Subnet, network string) []reconcile.Request {
	var requests []reconcile.Request
	for _, s := range subnets {
		if s.Spec.NetworkRef == network {
			requests = append(requests, requestOf(s.Namespace, s.Name))
		}
	}
	return requests
}

func ZonesOfSubnet(subnet v1alpha1.Subnet) []reconcile.Request {
	if subnet.Spec.DNSZoneRef == "" {
		return nil
	}
	return []reconcile.Request{requestOf(subnet.Namespace, subnet.Spec.DNSZoneRef)}
}

func SecurityGroupsOfNamespace(groups []v1alpha1.SecurityGroup) []reconcile.Request {
	return requestsFrom(groups)
}

func ReferrersOfGroup(groups []v1alpha1.SecurityGroup, name string) []reconcile.Request {
	var requests []reconcile.Request
	for _, g := range groups {
		rules := slices.Concat(g.Spec.Ingress, g.Spec.Egress)
		if slices.ContainsFunc(rules, func(r v1alpha1.SecurityGroupRule) bool { return r.SecurityGroupRef == name }) {
			requests = append(requests, requestOf(g.Namespace, g.Name))
		}
	}
	return requests
}

func GroupsOfVirtualMachine(vm v1alpha1.VirtualMachine) []reconcile.Request {
	var names []string
	for _, n := range vm.Spec.Networks {
		for _, g := range n.SecurityGroups {
			if !slices.Contains(names, g) {
				names = append(names, g)
			}
		}
	}
	requests := make([]reconcile.Request, len(names))
	for i, name := range names {
		requests[i] = requestOf(vm.Namespace, name)
	}
	return requests
}

func managedOnly() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetLabels()[LabelManaged] == "true" })
}

func generationOrDeletion() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectNew.GetGeneration() != e.ObjectOld.GetGeneration() || !e.ObjectNew.GetDeletionTimestamp().IsZero()
	}}
}

func ownerAnnotationHandler() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request { return OwnerRequests(obj) })
}

func inNamespaceOf(list func(context.Context, ...client.ListOption) []reconcile.Request) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		return list(ctx, client.InNamespace(obj.GetNamespace()))
	}
}

func elasticIPLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.ElasticIPList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func loadBalancerLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.LoadBalancerList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func internetGatewayLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.InternetGatewayList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func GatewaysOfNetwork(all []v1alpha1.InternetGateway, changed v1alpha1.InternetGateway) []reconcile.Request {
	var requests []reconcile.Request
	for _, other := range all {
		if other.UID != changed.UID && other.Spec.NetworkRef == changed.Spec.NetworkRef {
			requests = append(requests, requestOf(other.Namespace, other.Name))
		}
	}
	return requests
}

func dnsZoneLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.DNSZoneList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func networkLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.NetworkList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func subnetLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.SubnetList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func securityGroupLister(c client.Reader) func(context.Context, ...client.ListOption) []reconcile.Request {
	return func(ctx context.Context, opts ...client.ListOption) []reconcile.Request {
		var list v1alpha1.SecurityGroupList
		if err := c.List(ctx, &list, opts...); err != nil {
			return nil
		}
		return requestsFrom(list.Items)
	}
}

func everything(list func(context.Context, ...client.ListOption) []reconcile.Request) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request { return list(ctx) }
}

func TargetWaiters(all []v1alpha1.ElasticIP, gone v1alpha1.ElasticIP) []reconcile.Request {
	if gone.Spec.Target == nil {
		return nil
	}
	var requests []reconcile.Request
	for _, other := range all {
		sameTarget := other.Spec.Target != nil && other.Spec.Target.Kind == gone.Spec.Target.Kind && other.Spec.Target.Name == gone.Spec.Target.Name
		if other.UID != gone.UID && other.Namespace == gone.Namespace && sameTarget {
			requests = append(requests, requestOf(other.Namespace, other.Name))
		}
	}
	return requests
}

func HolderWaiters(all []v1alpha1.ElasticIP, gone v1alpha1.ElasticIP) []reconcile.Request {
	return append(AddressWaiters(all, gone), TargetWaiters(all, gone)...)
}

func AddressWaiters(all []v1alpha1.ElasticIP, gone v1alpha1.ElasticIP) []reconcile.Request {
	var requests []reconcile.Request
	for _, other := range all {
		sameAddress := other.Spec.Address != "" && (other.Spec.Address == gone.Spec.Address || other.Spec.Address == gone.Status.Address)
		if other.UID != gone.UID && PoolOf(other) == PoolOf(gone) && sameAddress {
			requests = append(requests, requestOf(other.Namespace, other.Name))
		}
	}
	return requests
}
