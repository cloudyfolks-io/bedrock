package compute

import (
	"context"
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const (
	refsIndex       = "compute.refs"
	observeInterval = 5 * time.Minute
)

type observation struct {
	Phase      string
	Node       string
	Addresses  []v1alpha1.VMAddress
	ElasticIP  string
	GuestAgent *v1alpha1.VMGuestAgent
	KubeVirt   ObservedVM
}

func Refs(vm v1alpha1.VirtualMachine) []string {
	var keys []string
	if vm.Spec.Image != nil {
		keys = append(keys, "image/"+imageScope(*vm.Spec.Image)+"/"+vm.Spec.Image.Name)
	}
	for _, network := range EffectiveNetworks(vm) {
		keys = append(keys, "subnet/"+network.SubnetRef)
		for _, group := range network.SecurityGroups {
			keys = append(keys, "securitygroup/"+group)
		}
	}
	for _, cdrom := range vm.Spec.CDROMs {
		keys = append(keys, "image/project/"+cdrom.ImageRef, "image/cluster/"+cdrom.ImageRef)
	}
	for _, key := range vm.Spec.SSHKeys {
		keys = append(keys, "sshkey/"+key)
	}
	for _, volume := range v1alpha1.VolumeRefs(vm) {
		keys = append(keys, "volume/"+volume)
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

func imageScope(ref v1alpha1.ImageRef) string {
	if ref.Scope == v1alpha1.ImageScopeProject {
		return v1alpha1.ImageScopeProject
	}
	return v1alpha1.ImageScopeCluster
}

func conditionStatuses(obj *unstructured.Unstructured) map[string]string {
	items, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	out := make(map[string]string, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["type"].(string)
		status, _ := entry["status"].(string)
		out[name] = status
	}
	return out
}

func failureMessage(obj *unstructured.Unstructured, fallback string) string {
	items, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if message, _ := entry["message"].(string); message != "" && (entry["type"] == "Failure" || entry["type"] == "Ready") {
			return message
		}
	}
	return fallback
}

func elasticIPFor(ips []v1alpha1.ElasticIP, vm string) string {
	matching := make([]v1alpha1.ElasticIP, 0, len(ips))
	for _, ip := range ips {
		target := ip.Spec.Target
		if target != nil && target.Kind == v1alpha1.ElasticIPTargetVirtualMachine && target.Name == vm {
			matching = append(matching, ip)
		}
	}
	if len(matching) == 0 {
		return ""
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Name < matching[j].Name })
	return matching[0].Status.Address
}

func expiredSnapshots(snapshots []unstructured.Unstructured, now time.Time) []unstructured.Unstructured {
	var expired []unstructured.Unstructured
	for _, snapshot := range snapshots {
		raw, found := snapshot.GetAnnotations()[AnnotationExpiresAt]
		if !found {
			continue
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err == nil && at.Before(now) {
			expired = append(expired, snapshot)
		}
	}
	return expired
}

func (r *VirtualMachineReconciler) observe(ctx context.Context, vm v1alpha1.VirtualMachine) (observation, error) {
	obs := observation{}
	kubeVirt := emptyOf(kubeVirtVMGVK)
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&vm), kubeVirt); err != nil && !notFound(err) {
		return obs, err
	}
	printable, _, _ := unstructured.NestedString(kubeVirt.Object, "status", "printableStatus")
	obs.KubeVirt = ObservedVM{PrintableStatus: printable, Message: failureMessage(kubeVirt, printable), VMConditions: conditionStatuses(kubeVirt)}
	obs.Phase = Phase(printable, vm.Spec.Running)

	instance := emptyOf(kubeVirtVMIGVK)
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(&vm), instance)
	if err != nil && !notFound(err) {
		return obs, err
	}
	if err == nil {
		obs.Node, _, _ = unstructured.NestedString(instance.Object, "status", "nodeName")
		obs.KubeVirt.NodeName = obs.Node
		obs.KubeVirt.VMIConditions = conditionStatuses(instance)
	}

	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods, client.InNamespace(vm.Namespace), client.MatchingLabels{launcherPodLabel: vm.Name}); err != nil {
		return obs, err
	}
	subnets := make([]string, 0)
	for _, network := range EffectiveNetworks(vm) {
		subnets = append(subnets, network.SubnetRef)
	}
	obs.Addresses = AddressesOf(pods.Items, obs.Node, vm.Namespace, subnets)

	var ips v1alpha1.ElasticIPList
	if err := r.Client.List(ctx, &ips, client.InNamespace(vm.Namespace)); err != nil {
		return obs, err
	}
	obs.ElasticIP = elasticIPFor(ips.Items, vm.Name)
	obs.GuestAgent = r.guestAgent(ctx, vm, obs.KubeVirt.VMIConditions)
	return obs, nil
}

func (r *VirtualMachineReconciler) guestAgent(ctx context.Context, vm v1alpha1.VirtualMachine, vmiConditions map[string]string) *v1alpha1.VMGuestAgent {
	if vmiConditions["AgentConnected"] != "True" || r.GuestInfo == nil {
		return nil
	}
	info, err := r.GuestInfo(ctx, vm.Namespace, vm.Name)
	if err != nil {
		return vm.Status.GuestAgent
	}
	return &info
}

func (r *VirtualMachineReconciler) pruneSnapshots(ctx context.Context, vm v1alpha1.VirtualMachine) (bool, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(vmSnapshotGVK.GroupVersion().WithKind("VirtualMachineSnapshotList"))
	selector := client.MatchingLabels{v1alpha1.LabelKind: "VirtualMachine", v1alpha1.LabelName: vm.Name}
	if err := r.Client.List(ctx, list, client.InNamespace(vm.Namespace), selector); err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	expired := expiredSnapshots(list.Items, r.now())
	for index := range expired {
		if err := r.Client.Delete(ctx, &expired[index]); client.IgnoreNotFound(err) != nil {
			return false, err
		}
	}
	return len(expired) < len(list.Items), nil
}

func (r *VirtualMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	indexer := func(obj client.Object) []string { return Refs(*obj.(*v1alpha1.VirtualMachine)) }
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.VirtualMachine{}, refsIndex, indexer); err != nil {
		return err
	}
	if err := network.IndexFabricIPs(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("compute-virtualmachine").
		For(&v1alpha1.VirtualMachine{}).
		Owns(emptyOf(kubeVirtVMGVK)).
		Owns(emptyOf(dataVolumeGVK)).
		Watches(emptyOf(kubeVirtVMIGVK), handler.EnqueueRequestsFromMapFunc(sameName)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(launcherOwner)).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(r.referrers("subnet/"))).
		Watches(&v1alpha1.SecurityGroup{}, handler.EnqueueRequestsFromMapFunc(r.referrers("securitygroup/"))).
		Watches(&v1alpha1.SSHKey{}, handler.EnqueueRequestsFromMapFunc(r.referrers("sshkey/"))).
		Watches(&v1alpha1.Volume{}, handler.EnqueueRequestsFromMapFunc(r.referrers("volume/"))).
		Watches(&v1alpha1.Image{}, handler.EnqueueRequestsFromMapFunc(r.referrers("image/project/"))).
		Watches(&v1alpha1.ClusterImage{}, handler.EnqueueRequestsFromMapFunc(r.referrers("image/cluster/"))).
		Watches(&v1alpha1.ElasticIP{}, handler.EnqueueRequestsFromMapFunc(targetedVM)).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(r.sizedByCustomCompute)).
		Complete(r)
}

func sameName(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
}

func launcherOwner(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[launcherPodLabel]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}}}
}

func targetedVM(_ context.Context, obj client.Object) []reconcile.Request {
	ip, ok := obj.(*v1alpha1.ElasticIP)
	if !ok || ip.Spec.Target == nil || ip.Spec.Target.Kind != v1alpha1.ElasticIPTargetVirtualMachine {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: ip.Namespace, Name: ip.Spec.Target.Name}}}
}

func (r *VirtualMachineReconciler) referrers(prefix string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		options := []client.ListOption{client.MatchingFields{refsIndex: prefix + obj.GetName()}}
		if obj.GetNamespace() != "" {
			options = append(options, client.InNamespace(obj.GetNamespace()))
		}
		var vms v1alpha1.VirtualMachineList
		if err := r.Client.List(ctx, &vms, options...); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(vms.Items))
		for _, vm := range vms.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&vm)})
		}
		return requests
	}
}
