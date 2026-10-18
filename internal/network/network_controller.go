package network

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const remainingSubnetsShown = 3

type NetworkReconciler struct {
	Client client.Client
	Reader client.Reader
}

func (r *NetworkReconciler) live() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

func (r *NetworkReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var network v1alpha1.Network
	if err := r.Client.Get(ctx, req.NamespacedName, &network); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !network.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &network)
	}
	if err := ensureFinalizer(ctx, r.Client, &network); err != nil {
		return reconcile.Result{}, err
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return r.publish(ctx, &network, notReadyStatus(network, ReasonVpcPending, err.Error()))
	}
	vrf, err := r.vrfID(ctx, &network, cfg)
	if err != nil {
		return reconcile.Result{}, err
	}
	spec, external := PlanVpc(VpcInput{Network: network, Config: cfg, VrfID: vrf})
	vpc := fabric.Vpc(fabric.Meta{Name: VpcName(network.Name), Labels: ObjectLabels("Network", network.Name, network.Spec.TenantRef, ""), Annotations: OwnerAnnotations(&network)}, spec)
	if conflict, err := r.conflicts(ctx, vpc, &network); err != nil || conflict {
		if err != nil {
			return reconcile.Result{}, err
		}
		return r.publish(ctx, &network, notReadyStatus(network, ReasonFabricConflict, fmt.Sprintf("a Fabric Vpc named %s belongs to another owner", vpc.GetName())))
	}
	if err := ssa.Apply(ctx, r.Client, vpc, v1alpha1.NetworkFieldManager); err != nil {
		return reconcile.Result{}, err
	}
	observed, err := getObject(ctx, r.Client, fabric.VpcGVK, vpc.GetName())
	if err != nil {
		return reconcile.Result{}, err
	}
	status, err := r.observedStatus(ctx, network, observed, spec, external, cfg)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, &network, status)
}

func (r *NetworkReconciler) conflicts(ctx context.Context, vpc *unstructured.Unstructured, network *v1alpha1.Network) (bool, error) {
	existing, err := getObject(ctx, r.Client, fabric.VpcGVK, vpc.GetName())
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !ownedBy(existing, network), nil
}

func (r *NetworkReconciler) vrfID(ctx context.Context, network *v1alpha1.Network, cfg Config) (uint32, error) {
	if cfg.EIPMode != EIPModeBGP || !network.Spec.EnableExternal {
		return 0, nil
	}
	if recorded, err := strconv.ParseUint(network.Annotations[AnnotationVrfID], 10, 32); err == nil {
		return uint32(recorded), nil
	}
	used, err := r.usedVrfIDs(ctx)
	if err != nil {
		return 0, err
	}
	id, err := NextVrfID(used)
	if err != nil {
		return 0, err
	}
	return r.recordVrfID(ctx, network, id)
}

func (r *NetworkReconciler) usedVrfIDs(ctx context.Context) ([]uint32, error) {
	var used []uint32
	vpcs, err := listObjects(ctx, r.Client, fabric.VpcGVK)
	if err != nil {
		return nil, err
	}
	for _, vpc := range vpcs {
		if id, found, _ := unstructured.NestedInt64(vpc.Object, "spec", "dynamicRouting", "vrfId"); found {
			used = append(used, uint32(id))
		}
	}
	var networks v1alpha1.NetworkList
	if err := r.live().List(ctx, &networks); err != nil {
		return nil, err
	}
	for _, n := range networks.Items {
		if id, err := strconv.ParseUint(n.Annotations[AnnotationVrfID], 10, 32); err == nil {
			used = append(used, uint32(id))
		}
	}
	return used, nil
}

func (r *NetworkReconciler) recordVrfID(ctx context.Context, network *v1alpha1.Network, id uint32) (uint32, error) {
	claimed := id
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.live().Get(ctx, client.ObjectKeyFromObject(network), network); err != nil {
			return err
		}
		if recorded, err := strconv.ParseUint(network.Annotations[AnnotationVrfID], 10, 32); err == nil {
			claimed = uint32(recorded)
			return nil
		}
		before := network.DeepCopy()
		network.Annotations = withAnnotation(network.Annotations, AnnotationVrfID, strconv.FormatUint(uint64(id), 10))
		return r.Client.Patch(ctx, network, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.NetworkFieldManager))
	})
	return claimed, err
}

func withAnnotation(annotations map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(annotations)+1)
	for k, v := range annotations {
		out[k] = v
	}
	out[key] = value
	return out
}

func (r *NetworkReconciler) observedStatus(ctx context.Context, network v1alpha1.Network, vpc *unstructured.Unstructured, spec fabric.VpcSpec, external External, cfg Config) (v1alpha1.NetworkStatus, error) {
	subnets, err := r.acceptedSubnets(ctx, network)
	if err != nil {
		return v1alpha1.NetworkStatus{}, err
	}
	conditions := network.Status.Conditions
	ready := falseCondition(v1alpha1.ConditionReady, network.Generation, ReasonVpcPending, "waiting for Fabric to program the Vpc")
	if fabric.VpcStandby(vpc) {
		ready = trueCondition(v1alpha1.ConditionReady, network.Generation, ReasonVpcReady)
	}
	conditions = withCondition(conditions, ready)
	if !external.Requested {
		meta.RemoveStatusCondition(&conditions, ConditionExternalReady)
	} else {
		condition, err := r.externalCondition(ctx, network, vpc, spec, external, cfg)
		if err != nil {
			return v1alpha1.NetworkStatus{}, err
		}
		conditions = withCondition(conditions, condition)
	}
	return v1alpha1.NetworkStatus{
		CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: network.Generation, Conditions: conditions},
		VPC:          vpc.GetName(),
		Subnets:      subnets,
	}, nil
}

func (r *NetworkReconciler) externalCondition(ctx context.Context, network v1alpha1.Network, vpc *unstructured.Unstructured, spec fabric.VpcSpec, external External, cfg Config) (metav1.Condition, error) {
	if external.Reason != ReasonConnected {
		return falseCondition(ConditionExternalReady, network.Generation, external.Reason, external.Message), nil
	}
	if message := r.externalGap(ctx, vpc, spec); message != "" {
		return falseCondition(ConditionExternalReady, network.Generation, ReasonPending, message), nil
	}
	gap, err := frrAgentGap(ctx, r.Client, cfg)
	if err != nil {
		return metav1.Condition{}, err
	}
	if gap != nil {
		return falseCondition(ConditionExternalReady, network.Generation, gap.Reason, gap.Message), nil
	}
	return trueCondition(ConditionExternalReady, network.Generation, ReasonConnected), nil
}

func (r *NetworkReconciler) externalGap(ctx context.Context, vpc *unstructured.Unstructured, spec fabric.VpcSpec) string {
	enabled, _, _ := unstructured.NestedBool(vpc.Object, "status", "enableExternal")
	if !enabled {
		return "Fabric has not enabled the external connection yet"
	}
	attached := fabric.VpcExternalSubnets(vpc)
	for _, subnet := range spec.ExtraExternalSubnets {
		if !containsString(attached, subnet) {
			return fmt.Sprintf("Fabric has not attached the external subnet %s yet", subnet)
		}
		port, err := getObject(ctx, r.Client, fabric.OvnEipGVK, vpc.GetName()+"-"+subnet)
		if err != nil || fabric.EipAddress(port) == "" || !fabric.EipReady(port) {
			return fmt.Sprintf("the router port of %s has no address yet", subnet)
		}
	}
	return ""
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (r *NetworkReconciler) acceptedSubnets(ctx context.Context, network v1alpha1.Network) ([]v1alpha1.NetworkSubnetStatus, error) {
	facts, err := ownedSubnetFacts(ctx, r.Client, network)
	if err != nil {
		return nil, err
	}
	accepted := AcceptedSubnets(facts, VerdictsFor(network.Spec.CIDR, facts))
	out := make([]v1alpha1.NetworkSubnetStatus, len(accepted))
	for i, s := range accepted {
		out[i] = v1alpha1.NetworkSubnetStatus{Namespace: s.Namespace, Name: s.Name, CIDR: s.CIDR}
	}
	return out, nil
}

func ownedSubnetFacts(ctx context.Context, c client.Reader, network v1alpha1.Network) ([]SubnetFacts, error) {
	var subnets v1alpha1.SubnetList
	if err := c.List(ctx, &subnets); err != nil {
		return nil, err
	}
	tenants := map[string]string{}
	var facts []SubnetFacts
	for _, s := range subnets.Items {
		if s.Spec.NetworkRef != network.Name {
			continue
		}
		tenant, known := tenants[s.Namespace]
		if !known {
			var namespace corev1.Namespace
			if err := c.Get(ctx, client.ObjectKey{Name: s.Namespace}, &namespace); client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			tenant = namespace.Labels[v1alpha1.LabelTenant]
			tenants[s.Namespace] = tenant
		}
		if NetworkAllowed(tenant, network) == nil {
			facts = append(facts, SubnetFacts{Namespace: s.Namespace, Name: s.Name, CIDR: s.Spec.CIDR, Created: s.CreationTimestamp.Time})
		}
	}
	return facts, nil
}

func notReadyStatus(network v1alpha1.Network, reason, message string) v1alpha1.NetworkStatus {
	status := network.Status
	status.ObservedGeneration = network.Generation
	status.Conditions = withCondition(network.Status.Conditions, falseCondition(v1alpha1.ConditionReady, network.Generation, reason, message))
	return status
}

func (r *NetworkReconciler) publish(ctx context.Context, network *v1alpha1.Network, status v1alpha1.NetworkStatus) (reconcile.Result, error) {
	body := &v1alpha1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: network.Name},
		Status:     status,
	}
	if err := ssa.ApplyStatus(ctx, r.Client, body, v1alpha1.NetworkFieldManager); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "write the Network status", "network", network.Name)
		return reconcile.Result{RequeueAfter: waitRequeue}, nil
	}
	if meta.IsStatusConditionTrue(status.Conditions, v1alpha1.ConditionReady) && externalSettled(status.Conditions) {
		return reconcile.Result{}, nil
	}
	return reconcile.Result{RequeueAfter: waitRequeue}, nil
}

func externalSettled(conditions []metav1.Condition) bool {
	external := meta.FindStatusCondition(conditions, ConditionExternalReady)
	return external == nil || external.Status == metav1.ConditionTrue
}

func (r *NetworkReconciler) finalize(ctx context.Context, network *v1alpha1.Network) (reconcile.Result, error) {
	facts, err := ownedSubnetFacts(ctx, r.Client, *network)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(facts) > 0 {
		status := notReadyStatus(*network, ReasonTerminating, remainingSubnets(facts))
		r.writeTerminatingStatus(ctx, network, status)
		return reconcile.Result{RequeueAfter: terminatingRequeue}, nil
	}
	gone, err := r.deleteOwnedVpc(ctx, network)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "delete the Vpc of the Network", "network", network.Name)
		return reconcile.Result{RequeueAfter: terminatingRequeue}, nil
	}
	if !gone {
		return reconcile.Result{RequeueAfter: terminatingRequeue}, nil
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, network)
}

func (r *NetworkReconciler) writeTerminatingStatus(ctx context.Context, network *v1alpha1.Network, status v1alpha1.NetworkStatus) {
	body := &v1alpha1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: network.Name},
		Status:     status,
	}
	if err := ssa.ApplyStatus(ctx, r.Client, body, v1alpha1.NetworkFieldManager); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "write the Network status", "network", network.Name)
	}
}

func remainingSubnets(facts []SubnetFacts) string {
	names := make([]string, 0, remainingSubnetsShown)
	for i, f := range facts {
		if i == remainingSubnetsShown {
			break
		}
		names = append(names, f.Namespace+"/"+f.Name)
	}
	return fmt.Sprintf("%d subnets remain: %s", len(facts), strings.Join(names, ", "))
}

func (r *NetworkReconciler) deleteOwnedVpc(ctx context.Context, network *v1alpha1.Network) (bool, error) {
	vpc, err := getObject(ctx, r.Client, fabric.VpcGVK, VpcName(network.Name))
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !ownedBy(vpc, network) {
		return true, nil
	}
	if err := client.IgnoreNotFound(r.Client.Delete(ctx, vpc)); err != nil {
		return false, err
	}
	present, err := exists(ctx, r.Client, fabric.VpcGVK, vpc.GetName())
	return !present, err
}
