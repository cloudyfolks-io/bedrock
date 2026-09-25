package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
	"github.com/cloudyfolks-labs/bedrock/internal/settings"
	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

const (
	DefaultAddonInterval      = 30 * time.Second
	DefaultAddonReadyInterval = 5 * time.Minute
)

type AddonInput struct {
	Cluster   v1alpha1.Cluster
	Hosts     []v1alpha1.Host
	Settings  map[string]string
	Bundle    release.Bundle
	CustomTLS *corev1.Secret
}

type Probe struct {
	GVK  schema.GroupVersionKind
	Key  client.ObjectKey
	Gate release.Gate
}

type Rendered struct {
	Objects     []*unstructured.Unstructured
	Probes      []Probe
	SkipReason  string
	SkipMessage string
}

type Addon struct {
	Name      string
	Condition string
	Render    func(AddonInput) (Rendered, error)
}

type AddonReconciler struct {
	Client        client.Client
	Bundle        release.Bundle
	Addons        []Addon
	Interval      time.Duration
	ReadyInterval time.Duration
}

func (r *AddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name != v1alpha1.ClusterName {
		return ctrl.Result{}, nil
	}
	var cluster v1alpha1.Cluster
	if err := r.Client.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cluster.Status.Version == "" {
		return ctrl.Result{RequeueAfter: r.Interval}, nil
	}
	input, err := r.input(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	conditions := make([]metav1.Condition, 0, len(r.Addons))
	allReady := true
	for _, addon := range r.Addons {
		condition := r.reconcileAddon(ctx, addon, input)
		condition.ObservedGeneration = cluster.Generation
		allReady = allReady && condition.Status == metav1.ConditionTrue
		conditions = append(conditions, condition)
	}
	if err := writeClusterStatus(ctx, r.Client, func(s *v1alpha1.ClusterStatus) {
		for _, condition := range conditions {
			v1alpha1.SetCondition(&s.Conditions, condition)
		}
	}); err != nil {
		return ctrl.Result{}, err
	}
	if allReady {
		return ctrl.Result{RequeueAfter: r.ReadyInterval}, nil
	}
	return ctrl.Result{RequeueAfter: r.Interval}, nil
}

func (r *AddonReconciler) input(ctx context.Context, cluster v1alpha1.Cluster) (AddonInput, error) {
	var hosts v1alpha1.HostList
	if err := r.Client.List(ctx, &hosts); err != nil {
		return AddonInput{}, err
	}
	values, err := settingValues(ctx, r.Client)
	if err != nil {
		return AddonInput{}, err
	}
	input := AddonInput{Cluster: cluster, Hosts: hosts.Items, Settings: values, Bundle: r.Bundle}
	if values["platform.tls-mode"] != "Custom" || values["platform.custom-tls"] == "" {
		return input, nil
	}
	var secret corev1.Secret
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: values["platform.custom-tls"]}, &secret)
	if errors.IsNotFound(err) {
		return input, nil
	}
	if err != nil {
		return AddonInput{}, err
	}
	input.CustomTLS = &secret
	return input, nil
}

func settingValues(ctx context.Context, c client.Client) (map[string]string, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, def := range settings.Catalog() {
		values[def.Key] = def.Default
	}
	for _, setting := range list.Items {
		if setting.Spec.Value != "" {
			values[setting.Name] = setting.Spec.Value
		}
	}
	return values, nil
}

func (r *AddonReconciler) reconcileAddon(ctx context.Context, addon Addon, input AddonInput) metav1.Condition {
	rendered, err := addon.Render(input)
	if err != nil {
		return addonCondition(addon, metav1.ConditionFalse, "RenderFailed", err.Error())
	}
	if rendered.SkipReason != "" {
		return addonCondition(addon, metav1.ConditionFalse, rendered.SkipReason, rendered.SkipMessage)
	}
	for _, obj := range rendered.Objects {
		err := ssa.Apply(ctx, r.Client, obj, v1alpha1.OperatorFieldManager)
		if meta.IsNoMatchError(err) {
			return addonCondition(addon, metav1.ConditionFalse, "MissingCRD", fmt.Sprintf("%s %s: %v", obj.GetKind(), obj.GetName(), err))
		}
		if err != nil {
			return addonCondition(addon, metav1.ConditionFalse, "ApplyFailed", fmt.Sprintf("%s %s: %v", obj.GetKind(), obj.GetName(), err))
		}
	}
	return r.probe(ctx, addon, rendered.Probes)
}

func (r *AddonReconciler) probe(ctx context.Context, addon Addon, probes []Probe) metav1.Condition {
	var pending []string
	for _, probe := range probes {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(probe.GVK)
		err := r.Client.Get(ctx, probe.Key, live)
		if errors.IsNotFound(err) || meta.IsNoMatchError(err) {
			pending = append(pending, fmt.Sprintf("%s %s not found yet", probe.GVK.Kind, probe.Key.Name))
			continue
		}
		if err != nil {
			return addonCondition(addon, metav1.ConditionFalse, "ProbeFailed", err.Error())
		}
		readiness := probe.Gate(live)
		if readiness.Failed {
			return addonCondition(addon, metav1.ConditionFalse, "Failed", readiness.Message)
		}
		if !readiness.Ready {
			pending = append(pending, readiness.Message)
		}
	}
	if len(pending) > 0 {
		return addonCondition(addon, metav1.ConditionFalse, "Progressing", strings.Join(pending, "; "))
	}
	return addonCondition(addon, metav1.ConditionTrue, "Ready", "")
}

func addonCondition(addon Addon, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: addon.Condition, Status: status, Reason: reason, Message: message}
}

func (r *AddonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toCluster := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: v1alpha1.ClusterName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("addons").
		For(&v1alpha1.Cluster{}).
		Watches(&v1alpha1.Host{}, toCluster).
		Watches(&v1alpha1.Setting{}, toCluster).
		Complete(r)
}
