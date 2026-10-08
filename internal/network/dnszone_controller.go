package network

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type DNSZoneReconciler struct {
	Client client.Client
	Kinds  KindProbe
}

func (r *DNSZoneReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var zone v1alpha1.DNSZone
	if err := r.Client.Get(ctx, req.NamespacedName, &zone); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !zone.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &zone)
	}
	if err := ensureFinalizer(ctx, r.Client, &zone); err != nil {
		return reconcile.Result{}, err
	}
	cond, requeue, err := r.program(ctx, zone)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, zone, cond, requeue), nil
}

func (r *DNSZoneReconciler) publish(ctx context.Context, zone v1alpha1.DNSZone, cond metav1.Condition, requeue time.Duration) reconcile.Result {
	body := &v1alpha1.DNSZone{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "DNSZone"},
		ObjectMeta: metav1.ObjectMeta{Namespace: zone.Namespace, Name: zone.Name},
		Status:     v1alpha1.DNSZoneStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: zone.Generation, Conditions: withCondition(zone.Status.Conditions, cond)}},
	}
	if err := applyStatus(ctx, r.Client, body); err != nil && requeue == 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}
	}
	return reconcile.Result{RequeueAfter: requeue}
}

func zoneRefused(zone v1alpha1.DNSZone, reason, message string) (metav1.Condition, time.Duration, error) {
	return falseCondition(v1alpha1.ConditionReady, zone.Generation, reason, message), 0, nil
}

func (r *DNSZoneReconciler) program(ctx context.Context, zone v1alpha1.DNSZone) (metav1.Condition, time.Duration, error) {
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: zone.Namespace}, &namespace); err != nil {
		return metav1.Condition{}, 0, err
	}
	tenant, project := namespace.Labels[v1alpha1.LabelTenant], namespace.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return zoneRefused(zone, ReasonNamespaceNotLabeled, "the namespace has no tenant and project labels; only project namespaces hold DNS zones")
	}
	if v := missingKind(r.Kinds, "DNSZone"); v != nil {
		return zoneRefused(zone, v.Reason, v.Message)
	}
	input := make([]Record, len(zone.Spec.Records))
	for i, rec := range zone.Spec.Records {
		input[i] = Record{Name: rec.Name, IPs: rec.IPs}
	}
	records, v := FabricRecords(zone.Spec.Zone, input)
	if v != nil {
		return zoneRefused(zone, v.Reason, v.Message)
	}
	vpcs, err := r.attachedVpcs(ctx, zone)
	if err != nil {
		return metav1.Condition{}, 0, err
	}
	return r.apply(ctx, zone, tenant, project, records, vpcs)
}

func (r *DNSZoneReconciler) attachedVpcs(ctx context.Context, zone v1alpha1.DNSZone) (map[string]string, error) {
	var subnets v1alpha1.SubnetList
	if err := r.Client.List(ctx, &subnets, client.InNamespace(zone.Namespace)); err != nil {
		return nil, err
	}
	vpcs := map[string]string{}
	for _, name := range NetworksOf(zone.Namespace, zone.Name, subnets.Items) {
		var network v1alpha1.Network
		err := r.Client.Get(ctx, client.ObjectKey{Name: name}, &network)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if err == nil && network.Status.VPC != "" {
			vpcs[name] = network.Status.VPC
		}
	}
	return vpcs, nil
}

func (r *DNSZoneReconciler) apply(ctx context.Context, zone v1alpha1.DNSZone, tenant, project string, records []fabric.DNSRecord, vpcs map[string]string) (metav1.Condition, time.Duration, error) {
	attached, err := r.attachedNetworkCount(ctx, zone)
	if err != nil {
		return metav1.Condition{}, 0, err
	}
	wanted := map[string]bool{}
	for network, vpc := range vpcs {
		name := DNSZoneName(zone.Namespace, zone.Name, network)
		if existing, err := getObject(ctx, r.Client, fabric.DNSZoneGVK, name); err == nil && !ownedBy(existing, &zone) {
			return zoneRefused(zone, ReasonFabricConflict, fmt.Sprintf("a Fabric DNSZone named %s belongs to another owner", name))
		}
		meta := fabric.Meta{Name: name, Labels: ObjectLabels("DNSZone", zone.Name, tenant, project), Annotations: OwnerAnnotations(&zone)}
		if err := ssa.Apply(ctx, r.Client, fabric.DNSZone(meta, vpc, records), v1alpha1.NetworkFieldManager); err != nil {
			return metav1.Condition{}, 0, err
		}
		wanted[name] = true
	}
	if err := r.deleteUnwanted(ctx, &zone, wanted); err != nil {
		return metav1.Condition{}, 0, err
	}
	switch {
	case attached == 0:
		return zoneRefused(zone, ReasonNotAttached, "no Subnet of this namespace names this zone in dnsZoneRef")
	case len(wanted) == 0:
		return falseCondition(v1alpha1.ConditionReady, zone.Generation, ReasonFabricNotReady, "the Networks of the Subnets have no Vpc yet"), waitRequeue, nil
	}
	for name := range wanted {
		observed, err := getObject(ctx, r.Client, fabric.DNSZoneGVK, name)
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		if !fabric.DNSZoneReady(observed) {
			return falseCondition(v1alpha1.ConditionReady, zone.Generation, ReasonFabricNotReady, "waiting for Fabric to program the zone"), waitRequeue, nil
		}
	}
	return trueCondition(v1alpha1.ConditionReady, zone.Generation, ReasonProgrammed), 0, nil
}

func (r *DNSZoneReconciler) attachedNetworkCount(ctx context.Context, zone v1alpha1.DNSZone) (int, error) {
	var subnets v1alpha1.SubnetList
	if err := r.Client.List(ctx, &subnets, client.InNamespace(zone.Namespace)); err != nil {
		return 0, err
	}
	return len(NetworksOf(zone.Namespace, zone.Name, subnets.Items)), nil
}

func (r *DNSZoneReconciler) ownedZones(ctx context.Context, zone *v1alpha1.DNSZone) ([]*unstructured.Unstructured, error) {
	zones, err := listObjects(ctx, r.Client, fabric.DNSZoneGVK)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(zones, func(z *unstructured.Unstructured) bool { return !ownedBy(z, zone) }), nil
}

func (r *DNSZoneReconciler) deleteUnwanted(ctx context.Context, zone *v1alpha1.DNSZone, wanted map[string]bool) error {
	owned, err := r.ownedZones(ctx, zone)
	if err != nil {
		return err
	}
	for _, z := range owned {
		if !wanted[z.GetName()] {
			if err := client.IgnoreNotFound(r.Client.Delete(ctx, z)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *DNSZoneReconciler) finalize(ctx context.Context, zone *v1alpha1.DNSZone) (reconcile.Result, error) {
	if err := r.deleteUnwanted(ctx, zone, nil); err != nil {
		return reconcile.Result{}, err
	}
	remaining, err := r.ownedZones(ctx, zone)
	if err != nil || len(remaining) > 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}, err
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, zone)
}
