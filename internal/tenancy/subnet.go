package tenancy

import (
	"context"
	"errors"
	"net/netip"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/util/retry"
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
	var claimed string
	var blocked Step
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		claimed, blocked, err = r.claimOnce(ctx, client.ObjectKeyFromObject(&project), tenant)
		return err
	})
	if err != nil {
		return "", failedStep(err), false
	}
	return claimed, blocked, claimed != ""
}

func (r *ProjectReconciler) claimOnce(ctx context.Context, key client.ObjectKey, tenant v1alpha1.Tenant) (string, Step, error) {
	var fresh v1alpha1.Project
	if err := r.Reader.Get(ctx, key, &fresh); err != nil {
		return "", Step{}, err
	}
	if fresh.Status.Subnet != "" {
		return fresh.Status.Subnet, Step{}, nil
	}
	vpc, err := netip.ParsePrefix(tenant.Spec.Network.VPCCIDR)
	if err != nil {
		return "", Step{}, err
	}
	var subnets v1alpha1.SubnetList
	var projects v1alpha1.ProjectList
	if err := errors.Join(r.Reader.List(ctx, &subnets), r.Reader.List(ctx, &projects)); err != nil {
		return "", Step{}, err
	}
	next, err := NextFreeSubnet(vpc, usedCIDRs(tenant.Name, subnets.Items, projects.Items))
	if errors.Is(err, ErrNoFreeSubnet) {
		return "", Step{Reason: ReasonCIDRExhausted, Message: err.Error()}, nil
	}
	if err != nil {
		return "", Step{}, err
	}
	base := fresh.DeepCopy()
	fresh.Status.Subnet = next.String()
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := r.Client.Status().Patch(ctx, &fresh, patch, client.FieldOwner(v1alpha1.TenancyFieldManager)); err != nil {
		return "", Step{}, err
	}
	return next.String(), Step{}, nil
}

func (r *ProjectReconciler) observeSubnet(ctx context.Context, desired v1alpha1.Subnet) *v1alpha1.Subnet {
	var subnet v1alpha1.Subnet
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(&desired), &subnet); err != nil {
		return nil
	}
	return &subnet
}
