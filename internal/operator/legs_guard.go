package operator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	reasonLegsGuarded  = "Guarded"
	reasonNoTenantLegs = "NoTenantLegs"
)

func (r *ClusterReconciler) reconcileTenantLegs(ctx context.Context) error {
	var cluster v1alpha1.Cluster
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	previous := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionTenantLegsGuarded)
	switch {
	case len(cluster.Spec.TenantLegs) > 0:
		return r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
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
