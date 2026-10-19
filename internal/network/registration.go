package network

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func fabricObject(kind string) client.Object {
	return newObject(fabric.VpcGVK.GroupVersion().WithKind(kind), "")
}

func settingsOfNetworkArea() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool { return strings.HasPrefix(obj.GetName(), settingPrefix) })
}

func (r *NetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).Named("network-network").
		For(&v1alpha1.Network{}).
		Watches(fabricObject("Vpc"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return NetworkOfSubnet(*obj.(*v1alpha1.Subnet))
		}), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(everything(networkLister(c))), builder.WithPredicates(settingsOfNetworkArea())).
		Watches(&appsv1.DaemonSet{}, handler.EnqueueRequestsFromMapFunc(everything(networkLister(c))), builder.WithPredicates(frrAgentOnly())).
		Complete(r)
}

func (r *SubnetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).Named("network-subnet").
		For(&v1alpha1.Subnet{}, builder.WithPredicates(generationOrDeletion())).
		Watches(&v1alpha1.Network{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var subnets v1alpha1.SubnetList
			if err := c.List(ctx, &subnets); err != nil {
				return nil
			}
			return SubnetsOfNetwork(subnets.Items, obj.GetName())
		})).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(everything(subnetLister(c))), builder.WithPredicates(settingsOfNetworkArea())).
		Watches(fabricObject("Subnet"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(newObject(attachmentGVK, ""), handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &v1alpha1.Subnet{}, handler.OnlyControllerOwner())).
		Watches(fabricObject("SecurityGroup"), handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			if obj.GetLabels()[v1alpha1.LabelKind] != "Namespace" {
				return nil
			}
			var subnets v1alpha1.SubnetList
			if err := c.List(ctx, &subnets, client.InNamespace(obj.GetLabels()[v1alpha1.LabelName])); err != nil {
				return nil
			}
			return requestsFrom(subnets.Items)
		}), builder.WithPredicates(managedOnly())).
		Complete(r)
}

func (r *SecurityGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	groups := securityGroupLister(c)
	return ctrl.NewControllerManagedBy(mgr).Named("network-securitygroup").
		For(&v1alpha1.SecurityGroup{}).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(groups))).
		Watches(&v1alpha1.SecurityGroup{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var list v1alpha1.SecurityGroupList
			if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
				return nil
			}
			return ReferrersOfGroup(list.Items, obj.GetName())
		})).
		Watches(&v1alpha1.VirtualMachine{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return GroupsOfVirtualMachine(*obj.(*v1alpha1.VirtualMachine))
		})).
		Watches(fabricObject("SecurityGroup"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(&networkingv1.NetworkPolicy{}, handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &v1alpha1.SecurityGroup{}, handler.OnlyControllerOwner())).
		Complete(r)
}

func deletionsOnly() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func hasTenantLabel() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetLabels()[v1alpha1.LabelTenant] != "" })
}

func (r *DefaultGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("network-defaultgroup").
		For(&corev1.Namespace{}, builder.WithPredicates(hasTenantLabel())).
		Watches(fabricObject("SecurityGroup"), handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			if obj.GetLabels()[v1alpha1.LabelKind] != "Namespace" {
				return nil
			}
			return []reconcile.Request{requestOf("", obj.GetLabels()[v1alpha1.LabelName])}
		}), builder.WithPredicates(managedOnly())).
		Complete(r)
}

func (r *ElasticIPReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	eips := elasticIPLister(c)
	ofIP := func(ctx context.Context, obj client.Object) []reconcile.Request {
		namespace, _, _ := fabric.IPOwner(obj.(*unstructured.Unstructured))
		return eips(ctx, client.InNamespace(namespace))
	}
	return ctrl.NewControllerManagedBy(mgr).Named("network-elasticip").
		For(&v1alpha1.ElasticIP{}).
		Watches(fabricObject("OvnEip"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(fabricObject("OvnFip"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(fabricObject("IP"), handler.EnqueueRequestsFromMapFunc(ofIP)).
		Watches(&v1alpha1.VirtualMachine{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(eips))).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(eips))).
		Watches(&v1alpha1.Network{}, handler.EnqueueRequestsFromMapFunc(everything(eips))).
		Watches(&v1alpha1.ElasticIP{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var list v1alpha1.ElasticIPList
			if err := c.List(ctx, &list); err != nil {
				return nil
			}
			return HolderWaiters(list.Items, *obj.(*v1alpha1.ElasticIP))
		})).
		Watches(&v1alpha1.ElasticIP{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var namespace corev1.Namespace
			if err := c.Get(ctx, client.ObjectKey{Name: obj.GetNamespace()}, &namespace); err != nil {
				return nil
			}
			return QuotaWaiters(ctx, c, namespace.Labels[v1alpha1.LabelTenant])
		}), builder.WithPredicates(deletionsOnly())).
		Watches(&v1alpha1.Tenant{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return QuotaWaiters(ctx, c, obj.GetName())
		}), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.LoadBalancer{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(eips))).
		Watches(&v1alpha1.InternetGateway{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(eips))).
		Watches(&appsv1.DaemonSet{}, handler.EnqueueRequestsFromMapFunc(everything(eips)), builder.WithPredicates(frrAgentOnly())).
		Complete(r)
}

func (r *LoadBalancerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	balancers := loadBalancerLister(c)
	ofIP := func(ctx context.Context, obj client.Object) []reconcile.Request {
		namespace, _, _ := fabric.IPOwner(obj.(*unstructured.Unstructured))
		return balancers(ctx, client.InNamespace(namespace))
	}
	return ctrl.NewControllerManagedBy(mgr).Named("network-loadbalancer").
		For(&v1alpha1.LoadBalancer{}).
		Watches(fabricObject("RouterLBRule"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(fabricObject("SwitchLBRule"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(fabricObject("Vip"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(&v1alpha1.ElasticIP{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(balancers))).
		Watches(fabricObject("IP"), handler.EnqueueRequestsFromMapFunc(ofIP)).
		Watches(&v1alpha1.VirtualMachine{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(balancers))).
		Complete(r)
}

func (r *InternetGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	gateways := internetGatewayLister(c)
	return ctrl.NewControllerManagedBy(mgr).Named("network-internetgateway").
		For(&v1alpha1.InternetGateway{}).
		Watches(fabricObject("OvnSnatRule"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Watches(&v1alpha1.ElasticIP{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(gateways))).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(inNamespaceOf(gateways))).
		Watches(&v1alpha1.Network{}, handler.EnqueueRequestsFromMapFunc(everything(gateways))).
		Watches(&v1alpha1.InternetGateway{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var list v1alpha1.InternetGatewayList
			if err := c.List(ctx, &list); err != nil {
				return nil
			}
			return GatewaysOfNetwork(list.Items, *obj.(*v1alpha1.InternetGateway))
		})).
		Complete(r)
}

func (r *DNSZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).Named("network-dnszone").
		For(&v1alpha1.DNSZone{}).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return ZonesOfSubnet(*obj.(*v1alpha1.Subnet))
		})).
		Watches(&v1alpha1.Network{}, handler.EnqueueRequestsFromMapFunc(everything(dnsZoneLister(c)))).
		Watches(fabricObject("DNSZone"), ownerAnnotationHandler(), builder.WithPredicates(managedOnly())).
		Complete(r)
}
