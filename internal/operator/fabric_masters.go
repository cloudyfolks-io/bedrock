package operator

import (
	"context"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const (
	ovnCentralDeployment           = "ovn-central"
	conditionFabricMastersApplied  = "FabricMastersApplied"
	fabricMastersAppliedAnnotation = "bedrock.cloudyfolks.io/fabric-masters-applied"
	fabricGroupName                = "fabric"
)

func (r *ClusterReconciler) reconcileFabricMasters(ctx context.Context) error {
	if !release.Uses(r.Bundle, release.VarMasterIPs) {
		return nil
	}
	var cluster v1alpha1.Cluster
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	vars, err := release.Vars(ctx, r.Client, cluster.Spec.API.VIP)
	if err != nil {
		return err
	}
	desired := vars[release.VarMasterIPs]
	if desired == "" {
		return nil
	}
	var central appsv1.Deployment
	err = r.APIReader.Get(ctx, client.ObjectKey{Namespace: fabricNamespace, Name: ovnCentralDeployment}, &central)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cluster.Annotations[fabricMastersAppliedAnnotation] == normalizedAddresses(desired) {
		return nil
	}
	if liveNodeIPs(central) == normalizedAddresses(desired) && centralReady(central) {
		return r.recordFabricMasters(ctx, cluster, desired)
	}
	env := r.upgradeEnv()
	err = release.InstallGroup(ctx, r.Client, r.Bundle, release.WithPodMTU(vars, cluster.Spec.PodMTU), r.Gates, r.Interval, r.GroupTimeout, fabricGroupName, beforeGroupHooks(r.Client), componentHook(env, cluster))
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		if r.Recorder != nil {
			r.Recorder.Event(&cluster, corev1.EventTypeWarning, "FabricMastersFailed", err.Error())
		}
		if writeErr := r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
			setCondition(s, conditionFabricMastersApplied, metav1.ConditionFalse, "ApplyFailed", err.Error(), cluster.Generation)
		}); writeErr != nil {
			return writeErr
		}
		return err
	}
	return r.recordFabricMasters(ctx, cluster, desired)
}

func (r *ClusterReconciler) recordFabricMasters(ctx context.Context, cluster v1alpha1.Cluster, desired string) error {
	marked := cluster.DeepCopy()
	if marked.Annotations == nil {
		marked.Annotations = map[string]string{}
	}
	marked.Annotations[fabricMastersAppliedAnnotation] = normalizedAddresses(desired)
	if err := r.Client.Patch(ctx, marked, client.MergeFrom(&cluster)); err != nil {
		return err
	}
	return r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
		setCondition(s, conditionFabricMastersApplied, metav1.ConditionTrue, "Applied", desired, cluster.Generation)
	})
}

func liveNodeIPs(central appsv1.Deployment) string {
	for _, container := range central.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "NODE_IPS" {
				return normalizedAddresses(env.Value)
			}
		}
	}
	return ""
}

func centralReady(central appsv1.Deployment) bool {
	want := int32(1)
	if central.Spec.Replicas != nil {
		want = *central.Spec.Replicas
	}
	status := central.Status
	return want > 0 && status.ObservedGeneration >= central.Generation && status.UpdatedReplicas == want && status.ReadyReplicas == want && status.Replicas == want
}

func normalizedAddresses(list string) string {
	parts := strings.Split(strings.ReplaceAll(list, " ", ""), ",")
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

func fabricMasterChange() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, okOld := e.ObjectOld.(*corev1.Node)
			newNode, okNew := e.ObjectNew.(*corev1.Node)
			return okOld && okNew && nodeKey(*oldNode) != nodeKey(*newNode)
		},
	}
}

func nodeKey(node corev1.Node) string {
	address := ""
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			address = addr.Address
			break
		}
	}
	return node.Labels[roles.FabricRoleLabel] + "|" + address
}
