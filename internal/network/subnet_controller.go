package network

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type SubnetReconciler struct {
	Client client.Client
}

func (r *SubnetReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var subnet v1alpha1.Subnet
	if err := r.Client.Get(ctx, req.NamespacedName, &subnet); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !subnet.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &subnet)
	}
	if err := ensureFinalizer(ctx, r.Client, &subnet); err != nil {
		return reconcile.Result{}, err
	}
	in, violation, err := r.check(ctx, subnet)
	if err != nil {
		return reconcile.Result{}, err
	}
	if violation != nil {
		return r.publish(ctx, subnet, violationCondition(v1alpha1.ConditionReady, subnet.Generation, violation), retryFor(violation))
	}
	desired, attachment := DesiredFabricSubnet(in), DesiredAttachment(in)
	if conflict, err := r.conflicts(ctx, desired, attachment, subnet); err != nil || conflict {
		if err != nil {
			return reconcile.Result{}, err
		}
		message := fmt.Sprintf("the Fabric Subnet %s or the attachment %s belongs to another owner", desired.GetName(), attachment.GetName())
		return r.publish(ctx, subnet, falseCondition(v1alpha1.ConditionReady, subnet.Generation, ReasonFabricConflict, message), 0)
	}
	for _, object := range []*unstructured.Unstructured{desired, attachment} {
		if err := ssa.Apply(ctx, r.Client, object, v1alpha1.NetworkFieldManager); err != nil {
			return reconcile.Result{}, err
		}
	}
	observed, err := getObject(ctx, r.Client, fabric.SubnetGVK, desired.GetName())
	if err != nil {
		return reconcile.Result{}, err
	}
	if !fabric.SubnetReady(observed) {
		message := fabric.SubnetErrorMessage(observed)
		if message == "" {
			message = "waiting for Fabric to program the Subnet"
		}
		return r.publish(ctx, subnet, falseCondition(v1alpha1.ConditionReady, subnet.Generation, ReasonFabricNotReady, message), waitRequeue)
	}
	defaults, err := getObject(ctx, r.Client, fabric.SecurityGroupGVK, DefaultSecurityGroupName(subnet.Namespace))
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	if err != nil || !fabric.SecurityGroupProgrammed(defaults) {
		return r.publish(ctx, subnet, falseCondition(v1alpha1.ConditionReady, subnet.Generation, ReasonWaitingForDefaultSecurityGroup, "waiting for Fabric to program the default security group of the namespace"), waitRequeue)
	}
	return r.publish(ctx, subnet, trueCondition(v1alpha1.ConditionReady, subnet.Generation, ReasonApplied), 0)
}

func retryFor(v *Violation) time.Duration {
	if v.Reason == ReasonNetworkNotReady {
		return waitRequeue
	}
	return 0
}

func (r *SubnetReconciler) check(ctx context.Context, subnet v1alpha1.Subnet) (SubnetInput, *Violation, error) {
	var network v1alpha1.Network
	err := r.Client.Get(ctx, client.ObjectKey{Name: subnet.Spec.NetworkRef}, &network)
	if apierrors.IsNotFound(err) {
		return SubnetInput{}, &Violation{Reason: ReasonNetworkNotFound, Message: fmt.Sprintf("the Network %q does not exist", subnet.Spec.NetworkRef)}, nil
	}
	if err != nil {
		return SubnetInput{}, nil, err
	}
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: subnet.Namespace}, &namespace); err != nil {
		return SubnetInput{}, nil, err
	}
	tenant, project := namespace.Labels[v1alpha1.LabelTenant], namespace.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return SubnetInput{}, &Violation{Reason: ReasonNamespaceNotLabeled, Message: "the namespace has no tenant and project labels; only project namespaces hold Subnets"}, nil
	}
	if v := NetworkAllowed(tenant, network); v != nil {
		return SubnetInput{}, v, nil
	}
	facts, err := ownedSubnetFacts(ctx, r.Client, network)
	if err != nil {
		return SubnetInput{}, nil, err
	}
	key := types.NamespacedName{Namespace: subnet.Namespace, Name: subnet.Name}
	if v := VerdictsFor(network.Spec.CIDR, facts)[key]; v != nil {
		return SubnetInput{}, v, nil
	}
	if v := GatewayViolation(subnet.Spec.CIDR, subnet.Spec.Gateway); v != nil {
		return SubnetInput{}, v, nil
	}
	if v := CheckExcludeIPs(subnet.Spec.CIDR, subnet.Spec.ExcludeIPs); v != nil {
		return SubnetInput{}, v, nil
	}
	if !v1alpha1.IsConditionTrue(network.Status.Conditions, v1alpha1.ConditionReady) {
		return SubnetInput{}, &Violation{Reason: ReasonNetworkNotReady, Message: fmt.Sprintf("the Network %q is not ready yet", network.Name)}, nil
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return SubnetInput{}, nil, err
	}
	return SubnetInput{Subnet: subnet, Network: network, Tenant: tenant, Project: project, DNSServers: cfg.DNSServers}, nil, nil
}

func (r *SubnetReconciler) conflicts(ctx context.Context, desired, attachment *unstructured.Unstructured, subnet v1alpha1.Subnet) (bool, error) {
	existing, err := getObject(ctx, r.Client, fabric.SubnetGVK, desired.GetName())
	if err == nil && !ownedBy(existing, &subnet) {
		return true, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	nad, err := r.attachment(ctx, subnet)
	if err == nil && !controlledBy(nad, subnet.UID) {
		return true, nil
	}
	return false, client.IgnoreNotFound(err)
}

func (r *SubnetReconciler) attachment(ctx context.Context, subnet v1alpha1.Subnet) (*unstructured.Unstructured, error) {
	nad := newObject(attachmentGVK, subnet.Name)
	return nad, r.Client.Get(ctx, client.ObjectKey{Namespace: subnet.Namespace, Name: subnet.Name}, nad)
}

func controlledBy(u *unstructured.Unstructured, uid types.UID) bool {
	for _, ref := range u.GetOwnerReferences() {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func (r *SubnetReconciler) publish(ctx context.Context, subnet v1alpha1.Subnet, cond metav1.Condition, requeue time.Duration) (reconcile.Result, error) {
	body := &v1alpha1.Subnet{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Subnet"},
		ObjectMeta: metav1.ObjectMeta{Namespace: subnet.Namespace, Name: subnet.Name},
		Status:     v1alpha1.SubnetStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: subnet.Generation, Conditions: withCondition(subnet.Status.Conditions, cond)}},
	}
	if err := applyStatus(ctx, r.Client, body); err != nil && requeue == 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}, nil
	}
	return reconcile.Result{RequeueAfter: requeue}, nil
}

func (r *SubnetReconciler) finalize(ctx context.Context, subnet *v1alpha1.Subnet) (reconcile.Result, error) {
	name := FabricName(subnet.Namespace, subnet.Name)
	fabricSubnet, err := getObject(ctx, r.Client, fabric.SubnetGVK, name)
	owned := err == nil && ownedBy(fabricSubnet, subnet)
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	if owned {
		if used := fabric.SubnetUsedAddresses(fabricSubnet); used > 0 {
			message := fmt.Sprintf("%d addresses are still in use; delete the VMs that use this Subnet", used)
			return r.publish(ctx, *subnet, falseCondition(v1alpha1.ConditionReady, subnet.Generation, ReasonInUse, message), terminatingRequeue)
		}
	}
	remaining, err := r.deleteOwned(ctx, subnet, name, owned)
	if err != nil {
		return reconcile.Result{}, err
	}
	if remaining {
		return reconcile.Result{RequeueAfter: terminatingRequeue}, nil
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, subnet)
}

func (r *SubnetReconciler) deleteOwned(ctx context.Context, subnet *v1alpha1.Subnet, fabricName string, ownsFabric bool) (bool, error) {
	nad, err := r.attachment(ctx, *subnet)
	nadOwned := err == nil && controlledBy(nad, subnet.UID)
	if nadOwned {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, nad)); err != nil {
			return false, err
		}
	}
	if ownsFabric {
		if err := deleteObject(ctx, r.Client, fabric.SubnetGVK, fabricName); err != nil {
			return false, err
		}
	}
	fabricPresent := false
	if ownsFabric {
		present, err := exists(ctx, r.Client, fabric.SubnetGVK, fabricName)
		if err != nil {
			return false, err
		}
		fabricPresent = present
	}
	nadPresent := false
	if nadOwned {
		_, err := r.attachment(ctx, *subnet)
		nadPresent = err == nil
	}
	return fabricPresent || nadPresent, nil
}
