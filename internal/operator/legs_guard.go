package operator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	reasonLegsGuarded  = "Guarded"
	reasonHostsOpen    = "HostsNotGuarded"
	reasonNoTenantLegs = "NoTenantLegs"
	reasonLegMissing   = "LegInterfaceMissing"
)

func hostGuardGaps(host v1alpha1.Host) (string, bool) {
	legs := meta.FindStatusCondition(host.Status.Conditions, v1alpha1.ConditionTenantLegsPresent)
	switch {
	case !host.Spec.Management.Enabled:
		return "management disabled", true
	case legs != nil && legs.Status == metav1.ConditionFalse && legs.Reason == reasonLegMissing:
		return "interface " + legs.Message, true
	case !meta.IsStatusConditionTrue(host.Status.Conditions, v1alpha1.ConditionManagementApplied):
		return "guard not applied", true
	case legs == nil:
		return "tenant legs not reported", true
	case legs.Status != metav1.ConditionTrue:
		return "tenant legs " + legs.Reason, true
	}
	return "", false
}

func (r *ClusterReconciler) hostsWithoutGuard(ctx context.Context) (string, []string, error) {
	var hosts v1alpha1.HostList
	if err := r.APIReader.List(ctx, &hosts); err != nil {
		return "", nil, err
	}
	reason := reasonHostsOpen
	var open []string
	for _, host := range hosts.Items {
		if gap, found := hostGuardGaps(host); found {
			if strings.HasPrefix(gap, "interface ") {
				reason = reasonLegMissing
			}
			open = append(open, fmt.Sprintf("host %s: %s", host.Name, gap))
		}
	}
	slices.Sort(open)
	return reason, open, nil
}

func (r *ClusterReconciler) reconcileTenantLegs(ctx context.Context) error {
	var cluster v1alpha1.Cluster
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	previous := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionTenantLegsGuarded)
	switch {
	case len(cluster.Spec.TenantLegs) > 0:
		reason, open, err := r.hostsWithoutGuard(ctx)
		if err != nil {
			return err
		}
		return r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
			if len(open) > 0 {
				setCondition(s, v1alpha1.ConditionTenantLegsGuarded, metav1.ConditionFalse, reason, "hosts without the guard: "+strings.Join(open, "; "), cluster.Generation)
				return
			}
			setCondition(s, v1alpha1.ConditionTenantLegsGuarded, metav1.ConditionTrue, reasonLegsGuarded, "", cluster.Generation)
		})
	case previous != nil && previous.Status == metav1.ConditionTrue:
		message := "spec.tenantLegs is empty: the hosts no longer guard the tenant-facing legs"
		if err := r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
			setCondition(s, v1alpha1.ConditionTenantLegsGuarded, metav1.ConditionFalse, reasonNoTenantLegs, message, cluster.Generation)
		}); err != nil {
			return err
		}
		if r.Recorder != nil {
			r.Recorder.Event(&cluster, corev1.EventTypeWarning, reasonNoTenantLegs, message)
		}
	}
	return nil
}
