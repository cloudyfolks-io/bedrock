package addons

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	conditionReady = "Ready"
	pruneInterval  = 5 * time.Second
)

type Reconciler struct {
	Client        client.Client
	Bundle        release.Bundle
	Interval      time.Duration
	ReadyInterval time.Duration
}

type outcome struct {
	ready   metav1.Condition
	waiting bool
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var addon v1alpha1.Addon
	if err := r.Client.Get(ctx, req.NamespacedName, &addon); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	def, known := Lookup(addon.Name)
	if !addon.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, addon)
	}
	if known && !controllerutil.ContainsFinalizer(&addon, Finalizer) {
		if err := r.addFinalizer(ctx, addon.Name); err != nil {
			return ctrl.Result{}, err
		}
	}
	input, err := r.gather(ctx, addon, def, known)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision := Decide(input.decision)
	result := r.act(ctx, addon, def, input, decision)
	r.writeStatus(ctx, addon, def, input, result.ready)
	return ctrl.Result{RequeueAfter: r.requeueAfter(result)}, nil
}

type gathered struct {
	decision DecisionInput
	values   map[string]string
	settings map[string]string
	hosts    []v1alpha1.Host
}

func (r *Reconciler) gather(ctx context.Context, addon v1alpha1.Addon, def Definition, known bool) (gathered, error) {
	input := gathered{decision: DecisionInput{Known: known, Enabled: addon.Spec.Enabled}}
	if !known {
		return input, nil
	}
	var cluster v1alpha1.Cluster
	if err := r.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); client.IgnoreNotFound(err) != nil {
		return input, err
	}
	input.decision.ClusterSettled = clusterSettled(cluster)
	var err error
	if input.settings, err = settingValues(ctx, r.Client); err != nil {
		return input, err
	}
	var hosts v1alpha1.HostList
	if err := r.Client.List(ctx, &hosts); err != nil {
		return input, err
	}
	input.hosts = hosts.Items
	given, err := valuesOf(addon)
	if err == nil {
		input.values, err = ResolveValues(def, given)
	}
	input.decision.ValuesErr = err
	if def.Preflight != nil {
		input.decision.Verdict = def.Preflight(input.hosts)
	}
	return input, nil
}

func clusterSettled(cluster v1alpha1.Cluster) bool {
	if cluster.Status.Version == "" {
		return false
	}
	upgrade := cluster.Status.Upgrade
	return upgrade == nil || upgrade.To == "" || upgrade.To == cluster.Status.Version
}

func (r *Reconciler) act(ctx context.Context, addon v1alpha1.Addon, def Definition, input gathered, decision Decision) outcome {
	switch decision.Action {
	case ActionApply:
		return r.apply(ctx, def, input)
	case ActionPrune:
		return r.prune(ctx, addon.Name, decision)
	}
	return outcome{ready: notReady(decision.Reason, decision.Message)}
}

func notReady(reason, message string) metav1.Condition {
	return metav1.Condition{Type: conditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: message}
}

func (r *Reconciler) apply(ctx context.Context, def Definition, input gathered) outcome {
	objects, ok := r.Bundle.Addons[def.Name]
	if !ok {
		return outcome{ready: notReady("RenderFailed", "the release has no manifests for addon "+def.Name)}
	}
	vars := map[string]string{}
	if def.Vars != nil {
		var err error
		if vars, err = def.Vars(input.values, input.settings); err != nil {
			return outcome{ready: notReady("RenderFailed", err.Error())}
		}
	}
	rendered, err := Render(def.Name, objects, vars)
	if err != nil {
		return outcome{ready: notReady("RenderFailed", err.Error())}
	}
	previous, err := readInventory(ctx, r.Client, def.Name)
	if err != nil {
		return outcome{ready: notReady("ApplyFailed", "inventory: "+err.Error())}
	}
	current := inventoryOf(rendered)
	if err := writeInventory(ctx, r.Client, def.Name, unionOf(previous, current)); err != nil {
		return outcome{ready: notReady("ApplyFailed", "inventory: "+err.Error())}
	}
	for _, obj := range rendered {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.AddonsFieldManager); err != nil {
			return outcome{ready: notReady(applyReason(err), fmt.Sprintf("apply %s %s: %v", obj.GetKind(), obj.GetName(), err))}
		}
	}
	if err := r.settle(ctx, def.Name, previous, current); err != nil {
		return outcome{ready: notReady("ApplyFailed", "inventory: "+err.Error())}
	}
	if pending := r.pendingWorkloads(ctx, rendered); len(pending) > 0 {
		return outcome{ready: notReady("Progressing", strings.Join(pending, "; "))}
	}
	return outcome{ready: metav1.Condition{Type: conditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "all workloads are ready"}}
}

func applyReason(err error) string {
	if meta.IsNoMatchError(err) {
		return "MissingCRD"
	}
	return "ApplyFailed"
}

func unionOf(a, b release.Inventory) release.Inventory {
	union := release.Inventory{}
	maps.Copy(union, a)
	maps.Copy(union, b)
	return union
}

func (r *Reconciler) settle(ctx context.Context, addon string, previous, current release.Inventory) error {
	if err := writeInventory(ctx, r.Client, addon, current); err != nil {
		return err
	}
	return release.Applier{Client: r.Client}.Prune(ctx, previous, current)
}

func (r *Reconciler) pendingWorkloads(ctx context.Context, rendered []*unstructured.Unstructured) []string {
	var pending []string
	for _, obj := range rendered {
		if !workloadKinds[obj.GetKind()] {
			continue
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GroupVersionKind())
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			pending = append(pending, fmt.Sprintf("%s %s: %v", obj.GetKind(), obj.GetName(), err))
			continue
		}
		if readiness := release.DefaultGate(live); !readiness.Ready {
			pending = append(pending, readiness.Message)
		}
	}
	return pending
}

func (r *Reconciler) prune(ctx context.Context, addon string, decision Decision) outcome {
	done, waiting, err := r.pruneOnce(ctx, addon)
	switch {
	case err != nil:
		return outcome{ready: notReady("ApplyFailed", "prune: "+err.Error()), waiting: true}
	case !done:
		return outcome{ready: notReady("Pruning", "waiting for pods: "+strings.Join(waiting, ", ")), waiting: true}
	}
	return outcome{ready: notReady(decision.Reason, decision.Message)}
}

func (r *Reconciler) pruneOnce(ctx context.Context, addon string) (bool, []string, error) {
	previous, err := readInventory(ctx, r.Client, addon)
	if err != nil {
		return false, nil, err
	}
	done, waiting, err := pruneStep(ctx, r.Client, previous, selectorsOf(r.Bundle.Addons[addon]))
	if err != nil || !done {
		return false, waiting, err
	}
	return true, nil, deleteInventory(ctx, r.Client, addon)
}

func (r *Reconciler) finalize(ctx context.Context, addon v1alpha1.Addon) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&addon, Finalizer) {
		return ctrl.Result{}, nil
	}
	done, waiting, err := r.pruneOnce(ctx, addon.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		def, _ := Lookup(addon.Name)
		r.writeStatus(ctx, addon, def, gathered{}, notReady("Pruning", "waiting for pods: "+strings.Join(waiting, ", ")))
		return ctrl.Result{RequeueAfter: pruneInterval}, nil
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, addon.Name)
}

func (r *Reconciler) addFinalizer(ctx context.Context, name string) error {
	return r.updateFinalizers(ctx, name, controllerutil.AddFinalizer)
}

func (r *Reconciler) removeFinalizer(ctx context.Context, name string) error {
	return r.updateFinalizers(ctx, name, controllerutil.RemoveFinalizer)
}

func (r *Reconciler) updateFinalizers(ctx context.Context, name string, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Addon
		if err := r.Client.Get(ctx, client.ObjectKey{Name: name}, &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !change(&current, Finalizer) {
			return nil
		}
		return r.Client.Update(ctx, &current, client.FieldOwner(v1alpha1.AddonsFieldManager))
	})
}

func (r *Reconciler) writeStatus(ctx context.Context, addon v1alpha1.Addon, def Definition, input gathered, ready metav1.Condition) {
	conditions := addon.Status.Conditions
	ready.ObservedGeneration = addon.Generation
	v1alpha1.SetCondition(&conditions, ready)
	if preflight, ok := preflightCondition(def, input, addon.Generation); ok {
		v1alpha1.SetCondition(&conditions, preflight)
	}
	status := &v1alpha1.Addon{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Addon"},
		ObjectMeta: metav1.ObjectMeta{Name: addon.Name},
		Status:     v1alpha1.AddonStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: addon.Generation, Conditions: conditions}},
	}
	if err := ssa.ApplyStatus(ctx, r.Client, status, v1alpha1.AddonsFieldManager); err != nil && !apierrors.IsNotFound(err) {
		log.FromContext(ctx).Error(err, "write addon status", "addon", addon.Name)
	}
}

func preflightCondition(def Definition, input gathered, generation int64) (metav1.Condition, bool) {
	if def.Preflight == nil || !input.decision.Known {
		return metav1.Condition{}, false
	}
	verdict := input.decision.Verdict
	status := map[VerdictState]metav1.ConditionStatus{VerdictPassed: metav1.ConditionTrue, VerdictFailed: metav1.ConditionFalse, VerdictUnknown: metav1.ConditionUnknown}[verdict.State]
	return metav1.Condition{Type: ConditionPreflight, Status: status, Reason: verdict.Reason, Message: verdict.Message, ObservedGeneration: generation}, true
}

func (r *Reconciler) requeueAfter(result outcome) time.Duration {
	switch {
	case result.waiting:
		return pruneInterval
	case result.ready.Status == metav1.ConditionTrue:
		return r.ReadyInterval
	}
	return r.Interval
}

func (r *Reconciler) allAddons(ctx context.Context, _ client.Object) []reconcile.Request {
	var addons v1alpha1.AddonList
	if err := r.Client.List(ctx, &addons); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(addons.Items))
	for _, addon := range addons.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: addon.Name}})
	}
	return requests
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	everyAddon := handler.EnqueueRequestsFromMapFunc(r.allAddons)
	return ctrl.NewControllerManagedBy(mgr).
		Named("addon").
		For(&v1alpha1.Addon{}).
		Watches(&v1alpha1.Host{}, everyAddon, builder.WithPredicates(hostPreflightPredicate())).
		Watches(&v1alpha1.Setting{}, everyAddon, builder.WithPredicates(readSettingPredicate())).
		Complete(r)
}
