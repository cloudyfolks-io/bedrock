package operator

import (
	"context"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const (
	ovnCentralDeployment = "ovn-central"
	ovnCentralNodeIPsEnv = "NODE_IPS"
	fabricGroupName      = "fabric"
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
	if sameAddresses(liveNodeIPs(central), desired) {
		return nil
	}
	env := r.upgradeEnv()
	return release.InstallGroup(ctx, r.Client, r.Bundle, release.WithPodMTU(vars, cluster.Spec.PodMTU), r.Gates, r.Interval, r.GroupTimeout, fabricGroupName, beforeGroupHooks(r.Client), componentHook(env, cluster))
}

func liveNodeIPs(central appsv1.Deployment) string {
	for _, container := range central.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == ovnCentralNodeIPsEnv {
				return env.Value
			}
		}
	}
	return ""
}

func sameAddresses(live, desired string) bool {
	split := func(list string) []string {
		parts := strings.Split(strings.ReplaceAll(list, " ", ""), ",")
		slices.Sort(parts)
		return parts
	}
	return slices.Equal(split(live), split(desired))
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
