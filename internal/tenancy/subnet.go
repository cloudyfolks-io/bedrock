package tenancy

import (
	"context"
	"errors"
	"net/netip"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func usedCIDRs(tenant string, subnets []v1alpha1.Subnet, projects []v1alpha1.Project) []netip.Prefix {
	used := []netip.Prefix{}
	for _, subnet := range subnets {
		if subnet.Spec.NetworkRef != v1alpha1.DefaultNetworkName(tenant) {
			continue
		}
		if prefix, err := netip.ParsePrefix(subnet.Spec.CIDR); err == nil {
			used = append(used, prefix)
		}
	}
	for _, project := range projects {
		if project.Spec.TenantRef != tenant {
			continue
		}
		if prefix, err := netip.ParsePrefix(project.Status.Subnet); err == nil {
			used = append(used, prefix)
		}
	}
	return used
}

func subnetStep(subnet *v1alpha1.Subnet, applyErr error) Step {
	switch {
	case applyErr != nil:
		return failedStep(applyErr)
	case subnet == nil:
		return Step{Reason: ReasonWaitingForSubnet, Message: "subnet not found yet"}
	case v1alpha1.IsConditionTrue(subnet.Status.Conditions, v1alpha1.ConditionReady):
		return Step{Ready: true, Reason: ReasonReady}
	}
	if ready := meta.FindStatusCondition(subnet.Status.Conditions, v1alpha1.ConditionReady); ready != nil && ready.Message != "" {
		return Step{Reason: ReasonWaitingForSubnet, Message: ready.Message}
	}
	return Step{Reason: ReasonWaitingForSubnet, Message: "subnet has no status yet"}
}

func (r *ProjectReconciler) ensureSubnet(ctx context.Context, project v1alpha1.Project, tenant v1alpha1.Tenant) (string, Step) {
	cidr := project.Status.Subnet
	if cidr == "" {
		claimed, step, ok := r.claimSubnet(ctx, project, tenant)
		if !ok {
			return "", step
		}
		cidr = claimed
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return cidr, failedStep(err)
	}
	desired := DesiredSubnet(project, prefix)
	applyErr := ssa.Apply(ctx, r.Client, WithOwner(&desired, &project, projectGVK), v1alpha1.TenancyFieldManager)
	return cidr, subnetStep(r.observeSubnet(ctx, desired), applyErr)
}

func (r *ProjectReconciler) claimSubnet(ctx context.Context, project v1alpha1.Project, tenant v1alpha1.Tenant) (string, Step, bool) {
	vpc, err := netip.ParsePrefix(tenant.Spec.Network.VPCCIDR)
	if err != nil {
		return "", failedStep(err), false
	}
	var subnets v1alpha1.SubnetList
	var projects v1alpha1.ProjectList
	if err := errors.Join(r.Reader.List(ctx, &subnets), r.Reader.List(ctx, &projects)); err != nil {
		return "", failedStep(err), false
	}
	next, err := NextFreeSubnet(vpc, usedCIDRs(tenant.Name, subnets.Items, projects.Items))
	if errors.Is(err, ErrNoFreeSubnet) {
		return "", Step{Reason: ReasonCIDRExhausted, Message: err.Error()}, false
	}
	if err != nil {
		return "", failedStep(err), false
	}
	claimed := project.DeepCopy()
	claimed.Status.Subnet = next.String()
	patch := client.MergeFrom(project.DeepCopy())
	if err := r.Client.Status().Patch(ctx, claimed, patch, client.FieldOwner(v1alpha1.TenancyFieldManager)); err != nil {
		return "", failedStep(err), false
	}
	return next.String(), Step{}, true
}

func (r *ProjectReconciler) observeSubnet(ctx context.Context, desired v1alpha1.Subnet) *v1alpha1.Subnet {
	var subnet v1alpha1.Subnet
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(&desired), &subnet); err != nil {
		return nil
	}
	return &subnet
}
