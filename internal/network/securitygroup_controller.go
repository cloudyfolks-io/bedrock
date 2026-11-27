package network

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type SecurityGroupReconciler struct {
	Client client.Client
}

type DefaultGroupReconciler struct {
	Client client.Client
}

func referencedGroups(rules ...[]Rule) []string {
	var refs []string
	for _, list := range rules {
		for _, r := range list {
			if r.SecurityGroup != "" && !slices.Contains(refs, r.SecurityGroup) {
				refs = append(refs, r.SecurityGroup)
			}
		}
	}
	slices.Sort(refs)
	return refs
}

func (r *SecurityGroupReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var group v1alpha1.SecurityGroup
	if err := r.Client.Get(ctx, req.NamespacedName, &group); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !group.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &group)
	}
	if err := ensureFinalizer(ctx, r.Client, &group); err != nil {
		return reconcile.Result{}, err
	}
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: group.Namespace}, &namespace); err != nil {
		return reconcile.Result{}, err
	}
	tenant, project := namespace.Labels[v1alpha1.LabelTenant], namespace.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return r.publish(ctx, group, falseCondition(v1alpha1.ConditionReady, group.Generation, ReasonNamespaceNotLabeled, "the namespace has no tenant and project labels; only project namespaces hold security groups"), 0)
	}
	ingress, egress := NormalizeRules(group.Spec.Ingress), NormalizeRules(group.Spec.Egress)
	fabricIngress, violation := FabricRules(group.Namespace, ingress)
	if violation == nil {
		var fabricEgress []fabric.SGRule
		fabricEgress, violation = FabricRules(group.Namespace, egress)
		if violation == nil {
			return r.program(ctx, group, tenant, project, ingress, egress, fabricIngress, EgressOrDefault(fabricEgress))
		}
	}
	return r.publish(ctx, group, violationCondition(v1alpha1.ConditionReady, group.Generation, violation), 0)
}

func (r *SecurityGroupReconciler) program(ctx context.Context, group v1alpha1.SecurityGroup, tenant, project string, ingress, egress []Rule, fabricIngress, fabricEgress []fabric.SGRule) (reconcile.Result, error) {
	owners, missing, err := r.referenced(ctx, group, referencedGroups(ingress, egress))
	if err != nil {
		return reconcile.Result{}, err
	}
	if missing != "" {
		return r.publish(ctx, group, falseCondition(v1alpha1.ConditionReady, group.Generation, ReasonRefNotFound, fmt.Sprintf("the security group %q does not exist in this namespace", missing)), 0)
	}
	name := FabricName(group.Namespace, group.Name)
	existing, err := getObject(ctx, r.Client, fabric.SecurityGroupGVK, name)
	if err == nil && !ownedBy(existing, &group) {
		return r.publish(ctx, group, falseCondition(v1alpha1.ConditionReady, group.Generation, ReasonFabricConflict, fmt.Sprintf("a Fabric security group named %s belongs to another owner", name)), 0)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	if err := r.applyShells(ctx, group, owners, tenant, project); err != nil {
		return reconcile.Result{}, err
	}
	meta := fabric.Meta{Name: name, Labels: ObjectLabels("SecurityGroup", group.Name, tenant, project), Annotations: OwnerAnnotations(&group)}
	if err := ssa.Apply(ctx, r.Client, fabric.SecurityGroup(meta, fabricIngress, fabricEgress), v1alpha1.NetworkFieldManager); err != nil {
		return reconcile.Result{}, err
	}
	if err := r.applyPolicy(ctx, group, tenant, project, ingress, egress); err != nil {
		return reconcile.Result{}, err
	}
	observed, err := getObject(ctx, r.Client, fabric.SecurityGroupGVK, name)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !fabric.SecurityGroupProgrammed(observed) {
		return r.publish(ctx, group, falseCondition(v1alpha1.ConditionReady, group.Generation, ReasonFabricNotReady, "waiting for Fabric to program the security group"), waitRequeue)
	}
	return r.publish(ctx, group, trueCondition(v1alpha1.ConditionReady, group.Generation, ReasonProgrammed), 0)
}

func (r *SecurityGroupReconciler) referenced(ctx context.Context, group v1alpha1.SecurityGroup, refs []string) (map[string]v1alpha1.SecurityGroup, string, error) {
	owners := make(map[string]v1alpha1.SecurityGroup, len(refs))
	for _, ref := range refs {
		var other v1alpha1.SecurityGroup
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: group.Namespace, Name: ref}, &other)
		if apierrors.IsNotFound(err) {
			return nil, ref, nil
		}
		if err != nil {
			return nil, "", err
		}
		owners[ref] = other
	}
	return owners, "", nil
}

func (r *SecurityGroupReconciler) applyShells(ctx context.Context, group v1alpha1.SecurityGroup, owners map[string]v1alpha1.SecurityGroup, tenant, project string) error {
	for ref, owner := range owners {
		if ref == group.Name {
			continue
		}
		name := FabricName(group.Namespace, ref)
		present, err := exists(ctx, r.Client, fabric.SecurityGroupGVK, name)
		if err != nil || present {
			if err != nil {
				return err
			}
			continue
		}
		shell := fabric.SecurityGroup(fabric.Meta{Name: name, Labels: ObjectLabels("SecurityGroup", ref, tenant, project), Annotations: OwnerAnnotations(&owner)}, nil, nil)
		if err := ssa.Apply(ctx, r.Client, shell, v1alpha1.NetworkFieldManager); err != nil {
			return err
		}
	}
	return nil
}

func (r *SecurityGroupReconciler) applyPolicy(ctx context.Context, group v1alpha1.SecurityGroup, tenant, project string, ingress, egress []Rule) error {
	var subnets v1alpha1.SubnetList
	if err := r.Client.List(ctx, &subnets, client.InNamespace(group.Namespace)); err != nil {
		return err
	}
	names := make([]string, len(subnets.Items))
	for i, s := range subnets.Items {
		names[i] = s.Name
	}
	np := PolicyFor(group.Namespace, group.Name, ingress, egress, names)
	np.Labels = ObjectLabels("SecurityGroup", group.Name, tenant, project)
	np.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: v1alpha1.GroupVersion.String(), Kind: "SecurityGroup", Name: group.Name, UID: group.UID,
		Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
	}}
	return ssa.Apply(ctx, r.Client, np, v1alpha1.NetworkFieldManager)
}

func (r *SecurityGroupReconciler) publish(ctx context.Context, group v1alpha1.SecurityGroup, cond metav1.Condition, requeue time.Duration) (reconcile.Result, error) {
	body := &v1alpha1.SecurityGroup{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SecurityGroup"},
		ObjectMeta: metav1.ObjectMeta{Namespace: group.Namespace, Name: group.Name},
		Status:     v1alpha1.SecurityGroupStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: group.Generation, Conditions: withCondition(group.Status.Conditions, cond)}},
	}
	if err := applyStatus(ctx, r.Client, body); err != nil && requeue == 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}, nil
	}
	return reconcile.Result{RequeueAfter: requeue}, nil
}

func (r *SecurityGroupReconciler) usedBy(ctx context.Context, group *v1alpha1.SecurityGroup) ([]string, error) {
	var vms v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &vms, client.InNamespace(group.Namespace)); err != nil {
		return nil, err
	}
	var users []string
	for _, vm := range vms.Items {
		for _, n := range vm.Spec.Networks {
			if slices.Contains(n.SecurityGroups, group.Name) {
				users = append(users, "VirtualMachine/"+vm.Name)
				break
			}
		}
	}
	var containers v1alpha1.ContainerList
	if err := r.Client.List(ctx, &containers, client.InNamespace(group.Namespace)); err != nil {
		return nil, err
	}
	for _, container := range containers.Items {
		if slices.Contains(container.Spec.Network.SecurityGroups, group.Name) {
			users = append(users, "Container/"+container.Name)
		}
	}
	var clusters v1alpha1.KubernetesClusterList
	if err := r.Client.List(ctx, &clusters, client.InNamespace(group.Namespace)); err != nil {
		return nil, err
	}
	for _, cluster := range clusters.Items {
		if kubernetesClusterUses(cluster.Spec, group.Name) {
			users = append(users, "KubernetesCluster/"+cluster.Name)
		}
	}
	return users, nil
}

func kubernetesClusterUses(spec v1alpha1.KubernetesClusterSpec, group string) bool {
	if slices.Contains(spec.ControlPlane.SecurityGroups, group) {
		return true
	}
	return slices.ContainsFunc(spec.Workers, func(worker v1alpha1.KubernetesWorker) bool {
		return slices.Contains(worker.SecurityGroups, group)
	})
}

func (r *SecurityGroupReconciler) finalize(ctx context.Context, group *v1alpha1.SecurityGroup) (reconcile.Result, error) {
	users, err := r.usedBy(ctx, group)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(users) > 0 {
		message := fmt.Sprintf("%v use this security group; remove it from them first", users)
		return r.publish(ctx, *group, falseCondition(v1alpha1.ConditionReady, group.Generation, ReasonInUse, message), terminatingRequeue)
	}
	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: group.Namespace, Name: policyNamePrefix + group.Name}}
	if err := client.IgnoreNotFound(r.Client.Delete(ctx, np)); err != nil {
		return reconcile.Result{}, err
	}
	gone, err := r.deleteFabricGroup(ctx, group)
	if err != nil || !gone {
		return reconcile.Result{RequeueAfter: terminatingRequeue}, err
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, group)
}

func (r *SecurityGroupReconciler) deleteFabricGroup(ctx context.Context, group *v1alpha1.SecurityGroup) (bool, error) {
	name := FabricName(group.Namespace, group.Name)
	existing, err := getObject(ctx, r.Client, fabric.SecurityGroupGVK, name)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !ownedBy(existing, group) {
		return true, nil
	}
	if err := deleteObject(ctx, r.Client, fabric.SecurityGroupGVK, name); err != nil {
		return false, err
	}
	present, err := exists(ctx, r.Client, fabric.SecurityGroupGVK, name)
	return !present, err
}

func (r *DefaultGroupReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var namespace corev1.Namespace
	err := r.Client.Get(ctx, req.NamespacedName, &namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	tenant := namespace.Labels[v1alpha1.LabelTenant]
	if apierrors.IsNotFound(err) || !namespace.DeletionTimestamp.IsZero() || tenant == "" {
		return reconcile.Result{}, r.deleteDefault(ctx, req.Name)
	}
	labels := ObjectLabels("Namespace", namespace.Name, tenant, namespace.Labels[v1alpha1.LabelProject])
	return reconcile.Result{}, ssa.Apply(ctx, r.Client, DefaultSecurityGroup(namespace.Name, labels), v1alpha1.NetworkFieldManager)
}

func (r *DefaultGroupReconciler) deleteDefault(ctx context.Context, namespace string) error {
	existing, err := getObject(ctx, r.Client, fabric.SecurityGroupGVK, DefaultSecurityGroupName(namespace))
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	if existing.GetLabels()[LabelManaged] != "true" {
		return nil
	}
	return deleteObject(ctx, r.Client, fabric.SecurityGroupGVK, existing.GetName())
}
