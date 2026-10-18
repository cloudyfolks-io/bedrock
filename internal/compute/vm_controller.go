package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	catalogRetry      = time.Minute
	applyFailureRetry = 30 * time.Second
	launcherPodLabel  = "vm.kubevirt.io/name"
)

type GuestInfoFunc func(ctx context.Context, namespace, name string) (v1alpha1.VMGuestAgent, error)

type VirtualMachineReconciler struct {
	Client    client.Client
	Secrets   client.Reader
	Scheme    *runtime.Scheme
	GuestInfo GuestInfoFunc
	Now       func() time.Time
}

type outcome struct {
	Gate         *Gate
	RequeueAfter time.Duration
}

func (r *VirtualMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var vm v1alpha1.VirtualMachine
	if err := r.Client.Get(ctx, req.NamespacedName, &vm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !vm.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	done, err := r.converge(ctx, vm)
	if err != nil {
		return ctrl.Result{}, err
	}
	obs, err := r.observe(ctx, vm)
	if err != nil {
		return ctrl.Result{}, err
	}
	expiring := false
	if _, held := v1alpha1.RestoreHolder(&vm); !held {
		expiring, err = r.pruneSnapshots(ctx, vm)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.writeStatus(ctx, vm, done.Gate, obs); err != nil {
		return ctrl.Result{}, fmt.Errorf("write the virtual machine status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueAfter(done, obs, expiring)}, nil
}

func requeueAfter(done outcome, obs observation, expiring bool) time.Duration {
	if done.RequeueAfter > 0 {
		return done.RequeueAfter
	}
	if expiring || obs.Phase == v1alpha1.VMPhaseRunning || obs.Phase == v1alpha1.VMPhaseMigrating {
		return observeInterval
	}
	return 0
}

func (r *VirtualMachineReconciler) converge(ctx context.Context, vm v1alpha1.VirtualMachine) (outcome, error) {
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: vm.Namespace}, &ns); err != nil {
		return outcome{}, err
	}
	labels, ok := OwnerLabels("VirtualMachine", vm.Name, ns)
	if !ok {
		return outcome{Gate: gateOf(ReasonNotInProject, "the namespace is not a project namespace")}, nil
	}
	if holder, held := v1alpha1.RestoreHolder(&vm); held {
		return outcome{Gate: gateOf(ReasonRestoreInProgress, "the restore %q holds the virtual machine", holder)}, nil
	}
	if issues := ValidateVirtualMachine(vm); len(issues) > 0 {
		return outcome{Gate: gateOf(ReasonInvalidSpec, "%s", issueMessage(issues))}, nil
	}
	in, gate, err := r.resolve(ctx, vm, labels)
	if err != nil || gate != nil {
		return outcome{Gate: gate, RequeueAfter: retryFor(gate)}, err
	}
	return r.apply(ctx, in)
}

func (r *VirtualMachineReconciler) secretReader() client.Reader {
	if r.Secrets != nil {
		return r.Secrets
	}
	return r.Client
}

func retryFor(gate *Gate) time.Duration {
	if gate == nil {
		return 0
	}
	switch gate.Reason {
	case ReasonInstanceTypeNotFound, ReasonPreferenceNotFound:
		return catalogRetry
	case ReasonWaitingForDefaultGroup, ReasonAddressInUse:
		return applyFailureRetry
	}
	return 0
}

func (r *VirtualMachineReconciler) apply(ctx context.Context, in RenderInput) (outcome, error) {
	rendered, err := Render(in)
	if err != nil {
		return outcome{Gate: gateOf(ReasonInvalidSpec, "%s", err.Error())}, nil
	}
	err = r.applyRendered(ctx, in, rendered)
	var conflict nameConflict
	if errors.As(err, &conflict) {
		return outcome{Gate: gateOf(ReasonNameConflict, "%s", conflict.Error()), RequeueAfter: applyFailureRetry}, nil
	}
	if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
		logf.FromContext(ctx).Error(err, "the cluster refused an object of the virtual machine", "virtualMachine", client.ObjectKeyFromObject(&in.VirtualMachine))
		return outcome{Gate: gateOf(ReasonApplyFailed, "the cluster refused an object of the virtual machine"), RequeueAfter: applyFailureRetry}, nil
	}
	return outcome{}, err
}

func (r *VirtualMachineReconciler) applyRendered(ctx context.Context, in RenderInput, rendered Rendered) error {
	vm := in.VirtualMachine
	if err := guardOwnership(ctx, r.Client, schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, rendered.Secret); err != nil {
		return err
	}
	if err := ssa.Apply(ctx, r.Client, rendered.Secret, v1alpha1.ComputeFieldManager); err != nil {
		return fmt.Errorf("apply the cloud-init secret: %w", err)
	}
	claims := append([]*unstructured.Unstructured{rendered.RootClaim}, rendered.CDROMClaims...)
	for _, claim := range claims {
		if err := createIfAbsent(ctx, r.Client, claim); err != nil {
			return fmt.Errorf("create the claim %s: %w", claim.GetName(), err)
		}
	}
	if err := growClaim(ctx, r.Client, vm.Namespace, RootClaimName(vm.Name), vm.Spec.RootDisk.SizeGi); err != nil {
		return fmt.Errorf("grow the root claim: %w", err)
	}
	if err := guardOwnership(ctx, r.Client, kubeVirtVMGVK, rendered.VirtualMachine); err != nil {
		return err
	}
	if err := ssa.Apply(ctx, r.Client, rendered.VirtualMachine, v1alpha1.ComputeFieldManager); err != nil {
		return fmt.Errorf("apply the KubeVirt virtual machine: %w", err)
	}
	for _, claim := range claims {
		if err := labelClaim(ctx, r.Client, vm.Namespace, claim.GetName(), in.Labels); err != nil {
			return fmt.Errorf("label the claim %s: %w", claim.GetName(), err)
		}
	}
	return r.syncLauncherPods(ctx, vm, in.NICs)
}

type nameConflict struct{ Kind, Name string }

func (e nameConflict) Error() string {
	return fmt.Sprintf("the name of the %s %q is used by an object that this resource does not own", e.Kind, e.Name)
}

func controllerUID(obj metav1.Object) types.UID {
	if ref := metav1.GetControllerOf(obj); ref != nil {
		return ref.UID
	}
	return ""
}

var identityLabels = []string{v1alpha1.LabelKind, v1alpha1.LabelName, v1alpha1.LabelTenant, v1alpha1.LabelProject}

func isOrphanOf(existing, desired metav1.Object) bool {
	want := desired.GetLabels()
	if want[v1alpha1.LabelKind] == "" || controllerUID(existing) != "" {
		return false
	}
	for _, key := range identityLabels {
		if existing.GetLabels()[key] != want[key] {
			return false
		}
	}
	return true
}

func ensureOwned(existing, desired metav1.Object, kind string) error {
	want := controllerUID(desired)
	if want == "" || (controllerUID(existing) != want && !isOrphanOf(existing, desired)) {
		return nameConflict{Kind: kind, Name: desired.GetName()}
	}
	return nil
}

func adoptOrphan(ctx context.Context, c client.Client, existing, desired *unstructured.Unstructured) error {
	if !isOrphanOf(existing, desired) {
		return nil
	}
	patch, err := rawMergePatch(map[string]any{"metadata": map[string]any{"ownerReferences": desired.GetOwnerReferences()}})
	if err != nil {
		return err
	}
	return c.Patch(ctx, existing, patch, client.FieldOwner(v1alpha1.ComputeFieldManager))
}

func guardOwnership(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, desired metav1.Object) error {
	existing := emptyOf(gvk)
	err := c.Get(ctx, client.ObjectKey{Namespace: desired.GetNamespace(), Name: desired.GetName()}, existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return ensureOwned(existing, desired, gvk.Kind)
}

func createIfAbsent(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	existing := emptyOf(obj.GroupVersionKind())
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err == nil {
		if err := ensureOwned(existing, obj, obj.GetKind()); err != nil {
			return err
		}
		return adoptOrphan(ctx, c, existing, obj)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	err = c.Create(ctx, obj.DeepCopy(), client.FieldOwner(v1alpha1.ComputeFieldManager))
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	raced := emptyOf(obj.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), raced); err != nil {
		return err
	}
	if err := ensureOwned(raced, obj, obj.GetKind()); err != nil {
		return err
	}
	return adoptOrphan(ctx, c, raced, obj)
}

func rawMergePatch(body map[string]any) (client.Patch, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return client.RawPatch(types.MergePatchType, data), nil
}

func growClaim(ctx context.Context, c client.Client, namespace, name string, sizeGi int32) error {
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &pvc); err != nil {
		return client.IgnoreNotFound(err)
	}
	requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	desired := resource.MustParse(fmt.Sprintf("%dGi", sizeGi))
	if desired.Cmp(requested) <= 0 {
		return nil
	}
	patch, err := rawMergePatch(map[string]any{"spec": map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": desired.String()}}}})
	if err != nil {
		return err
	}
	return c.Patch(ctx, &pvc, patch, client.FieldOwner(v1alpha1.ComputeFieldManager))
}

func missingLabels(have, want map[string]string) map[string]any {
	missing := map[string]any{}
	for key, value := range want {
		if have[key] != value {
			missing[key] = value
		}
	}
	return missing
}

func labelClaim(ctx context.Context, c client.Client, namespace, name string, labels map[string]string) error {
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &pvc); err != nil {
		return client.IgnoreNotFound(err)
	}
	missing := missingLabels(pvc.Labels, labels)
	if len(missing) == 0 {
		return nil
	}
	patch, err := rawMergePatch(map[string]any{"metadata": map[string]any{"labels": missing}})
	if err != nil {
		return err
	}
	return c.Patch(ctx, &pvc, patch, client.FieldOwner(v1alpha1.ComputeFieldManager))
}

func (r *VirtualMachineReconciler) syncLauncherPods(ctx context.Context, vm v1alpha1.VirtualMachine, nics []NIC) error {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods, client.InNamespace(vm.Namespace), client.MatchingLabels{launcherPodLabel: vm.Name}); err != nil {
		return err
	}
	annotations, labels := network.PodNetworkMetadata(nics)
	for index := range pods.Items {
		pod := &pods.Items[index]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		body := podMetadataPatch(*pod, annotations, labels)
		if body == nil {
			continue
		}
		patch, err := rawMergePatch(body)
		if err != nil {
			return err
		}
		if err := r.Client.Patch(ctx, pod, patch, client.FieldOwner(v1alpha1.ComputeFieldManager)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func podMetadataPatch(pod corev1.Pod, annotations, labels map[string]string) map[string]any {
	changedAnnotations := missingLabels(pod.Annotations, annotations)
	changedLabels := missingLabels(pod.Labels, labels)
	for key := range pod.Labels {
		if _, wanted := labels[key]; !wanted && strings.HasPrefix(key, network.SecurityGroupLabel("")) {
			changedLabels[key] = nil
		}
	}
	metadata := map[string]any{}
	if len(changedAnnotations) > 0 {
		metadata["annotations"] = changedAnnotations
	}
	if len(changedLabels) > 0 {
		metadata["labels"] = changedLabels
	}
	if len(metadata) == 0 {
		return nil
	}
	return map[string]any{"metadata": metadata}
}

func (r *VirtualMachineReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func mergeConditions(old, desired []metav1.Condition, now time.Time) []metav1.Condition {
	out := make([]metav1.Condition, 0, len(desired))
	for _, cond := range desired {
		cond.LastTransitionTime = metav1.NewTime(now)
		for _, previous := range old {
			if previous.Type == cond.Type && previous.Status == cond.Status {
				cond.LastTransitionTime = previous.LastTransitionTime
			}
		}
		out = append(out, cond)
	}
	return out
}

func (r *VirtualMachineReconciler) writeStatus(ctx context.Context, vm v1alpha1.VirtualMachine, gate *Gate, obs observation) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.VirtualMachine
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&vm), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		desired.Status = statusOf(current, gate, obs, r.now())
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.ComputeFieldManager))
	})
}

func statusOf(vm v1alpha1.VirtualMachine, gate *Gate, obs observation, now time.Time) v1alpha1.VirtualMachineStatus {
	status := v1alpha1.VirtualMachineStatus{
		CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: vm.Generation},
		Phase:        obs.Phase,
		Node:         obs.Node,
		Addresses:    obs.Addresses,
		ElasticIP:    obs.ElasticIP,
		GuestAgent:   obs.GuestAgent,
		Console:      ConsoleFor(obs.Phase, vm.Namespace, vm.Name),
	}
	conditions := Conditions(vm.Generation, obs.Phase, vm.Spec.Running, gate, obs.KubeVirt)
	status.Conditions = mergeConditions(vm.Status.Conditions, conditions, now)
	return status
}
