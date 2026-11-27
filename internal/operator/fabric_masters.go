package operator

import (
	"context"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	if normalizedAddresses(liveNodeIPs(central)) == normalizedAddresses(desired) && centralSettled(central) {
		return r.recordFabricMasters(ctx, cluster, desired)
	}
	masters, err := release.MasterNodes(ctx, r.Client)
	if err != nil {
		return err
	}
	env := r.upgradeEnv()
	steps := growthSteps(central, masters)
	base := release.WithPodMTU(vars, cluster.Spec.PodMTU)
	err = r.growCentral(ctx, base, steps)
	if err == nil {
		err = release.UpgradeGroup(ctx, r.Client, r.Bundle, release.WithMasters(base, steps[len(steps)-1]), r.Gates, r.Interval, r.GroupTimeout, fabricGroupName, beforeGroupHooks(r.Client), prepareComponentRollout(env), componentHook(env, cluster))
	}
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

func (r *ClusterReconciler) growCentral(ctx context.Context, vars map[string]string, steps [][]release.MasterNode) error {
	centralOnly := onlyCentral(r.Bundle)
	for _, step := range steps {
		if err := release.InstallGroup(ctx, r.Client, centralOnly, release.WithMasters(vars, step), r.Gates, r.Interval, r.GroupTimeout, fabricGroupName, release.SkipHook, release.SkipHook); err != nil {
			return err
		}
		if err := r.waitRaftMembers(ctx, len(step)); err != nil {
			return err
		}
	}
	return nil
}

func onlyCentral(bundle release.Bundle) release.Bundle {
	groups := make([]release.Group, len(bundle.Groups))
	for i, group := range bundle.Groups {
		groups[i] = group
		if group.Name == fabricGroupName {
			groups[i].Objects = slices.DeleteFunc(slices.Clone(group.Objects), func(obj *unstructured.Unstructured) bool {
				return obj.GetKind() != "Deployment" || obj.GetName() != ovnCentralDeployment
			})
		}
	}
	return release.Bundle{Spec: bundle.Spec, Groups: groups}
}

func growthSteps(central appsv1.Deployment, masters []release.MasterNode) [][]release.MasterNode {
	current := splitAddresses(liveNodeIPs(central))
	if len(current) > 0 && int(centralReplicas(central)) != len(current) {
		current = current[:1]
	}
	if len(current) == 0 || len(current) >= len(masters) || !subset(current, addressesOf(masters)) {
		return [][]release.MasterNode{masters}
	}
	members := make([]release.MasterNode, 0, len(masters))
	for _, address := range current {
		members = append(members, masters[slices.IndexFunc(masters, func(m release.MasterNode) bool { return m.Address == address })])
	}
	var steps [][]release.MasterNode
	for _, master := range masters {
		if slices.Contains(current, master.Address) {
			continue
		}
		members = append(slices.Clone(members), master)
		steps = append(steps, members)
	}
	return steps
}

func addressesOf(masters []release.MasterNode) []string {
	out := make([]string, len(masters))
	for i, master := range masters {
		out[i] = master.Address
	}
	return out
}

func splitAddresses(list string) []string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(list, " ", ""), ",")
}

func subset(part, whole []string) bool {
	for _, address := range part {
		if !slices.Contains(whole, address) {
			return false
		}
	}
	return true
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
				return env.Value
			}
		}
	}
	return ""
}

func centralReplicas(central appsv1.Deployment) int32 {
	if central.Spec.Replicas == nil {
		return 1
	}
	return *central.Spec.Replicas
}

func centralSettled(central appsv1.Deployment) bool {
	want := centralReplicas(central)
	status := central.Status
	return int(want) == len(splitAddresses(liveNodeIPs(central))) && want > 0 && status.ObservedGeneration >= central.Generation && status.UpdatedReplicas == want && status.ReadyReplicas == want && status.Replicas == want
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
