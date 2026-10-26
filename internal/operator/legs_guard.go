package operator

import (
	"context"
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
)

func (r *ClusterReconciler) hostsWithoutGuard(ctx context.Context) ([]string, error) {
	var hosts v1alpha1.HostList
	if err := r.APIReader.List(ctx, &hosts); err != nil {
		return nil, err
	}
	var open []string
	for _, host := range hosts.Items {
		if host.Spec.Management.Enabled && !meta.IsStatusConditionTrue(host.Status.Conditions, v1alpha1.ConditionManagementApplied) {
			open = append(open, host.Name)
		}
	}
	slices.Sort(open)
	return open, nil
}

func (r *ClusterReconciler) reconcileTenantLegs(ctx context.Context) error {
	var cluster v1alpha1.Cluster
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	previous := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionTenantLegsGuarded)
	switch {
	case len(cluster.Spec.TenantLegs) > 0:
		open, err := r.hostsWithoutGuard(ctx)
		if err != nil {
			return err
		}
		return r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
			if len(open) > 0 {
				setCondition(s, v1alpha1.ConditionTenantLegsGuarded, metav1.ConditionFalse, reasonHostsOpen, "hosts without the applied guard: "+strings.Join(open, ", "), cluster.Generation)
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
