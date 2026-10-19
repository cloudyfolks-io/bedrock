package operator

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	DefaultAddonInterval      = 30 * time.Second
	DefaultAddonReadyInterval = 5 * time.Minute
)

type AddonInput struct {
	Cluster        v1alpha1.Cluster
	Hosts          []v1alpha1.Host
	Settings       map[string]string
	Bundle         release.Bundle
	CustomTLS      *corev1.Secret
	PlatformCA     *corev1.Secret
	CustomCA       []byte
	WebhookToken   string
	AuthnInstalled bool
	UploadProxyCA  []byte
	ProxyInstalled bool
	CephMonFSID    string
	CephProblems   []string
}

type Probe struct {
	GVK  schema.GroupVersionKind
	Key  client.ObjectKey
	Gate release.Gate
}

type Rendered struct {
	Objects     []*unstructured.Unstructured
	Absent      []*unstructured.Unstructured
	Probes      []Probe
	SkipReason  string
	SkipMessage string
	Conditions  []metav1.Condition
}

type Addon struct {
	Name      string
	Condition string
	Render    func(AddonInput) (Rendered, error)
	Advance   func(context.Context, client.Client, execFunc, AddonInput) []metav1.Condition
}

type AddonReconciler struct {
	Client        client.Client
	Bundle        release.Bundle
	Addons        []Addon
	Interval      time.Duration
	ReadyInterval time.Duration
	Exec          execFunc
}

func (r *AddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name != v1alpha1.ClusterName {
		return ctrl.Result{}, nil
	}
	var cluster v1alpha1.Cluster
	if err := r.Client.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cluster.Status.Version == "" || upgrading(cluster.Status) {
		return ctrl.Result{RequeueAfter: r.Interval}, nil
	}
	if err := ensureAuthnMaterial(ctx, r.Client, rand.Reader, time.Now()); err != nil {
		return ctrl.Result{}, err
	}
	input, err := r.input(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	conditions := make([]metav1.Condition, 0, len(r.Addons))
	allReady := true
	for _, addon := range r.Addons {
		condition, extra := r.reconcileAddon(ctx, addon, input)
		condition.ObservedGeneration = cluster.Generation
		allReady = allReady && condition.Status == metav1.ConditionTrue
		conditions = append(conditions, condition)
		for _, other := range extra {
			other.ObservedGeneration = cluster.Generation
			conditions = append(conditions, other)
		}
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
	platformCA, err := optionalSecret(ctx, r.Client, client.ObjectKey{Namespace: apiserver.CASecretNamespace, Name: apiserver.CASecretName})
	if err != nil {
		return AddonInput{}, err
	}
	token, err := optionalSecret(ctx, r.Client, client.ObjectKey{Namespace: release.SystemNamespace, Name: apiserver.TokenSecretName})
	if err != nil {
		return AddonInput{}, err
	}
	installed, err := deploymentExists(ctx, r.Client, client.ObjectKey{Namespace: release.SystemNamespace, Name: authnName})
	if err != nil {
		return AddonInput{}, err
	}
	uploadProxyCA, err := optionalConfigMapData(ctx, r.Client, client.ObjectKey{Namespace: uploadNamespace, Name: uploadProxyCAConfigMap}, uploadProxyCAKey)
	if err != nil {
		return AddonInput{}, err
	}
	proxyInstalled, err := deploymentExists(ctx, r.Client, client.ObjectKey{Namespace: release.SystemNamespace, Name: ProxyName})
	if err != nil {
		return AddonInput{}, err
	}
	monFSID, err := adoptedMonFSID(ctx, r.Client, cluster)
	if err != nil {
		return AddonInput{}, err
	}
	input := AddonInput{Cluster: cluster, Hosts: hosts.Items, Settings: values, Bundle: r.Bundle, PlatformCA: platformCA, WebhookToken: string(secretData(token, WebhookTokenKey)), AuthnInstalled: installed, UploadProxyCA: uploadProxyCA, ProxyInstalled: proxyInstalled, CephMonFSID: monFSID}
	input.CephProblems = adoptedProblems(ctx, r.Client, r.Exec, input)
	if values["platform.tls-mode"] != "Custom" || values["platform.custom-tls"] == "" {
		return input, nil
	}
	customTLS, err := optionalSecret(ctx, r.Client, client.ObjectKey{Namespace: release.SystemNamespace, Name: values["platform.custom-tls"]})
	if err != nil {
		return AddonInput{}, err
	}
	input.CustomTLS = customTLS
	input.CustomCA = secretData(customTLS, "ca.crt")
	return input, nil
}

func optionalSecret(ctx context.Context, c client.Reader, key client.ObjectKey) (*corev1.Secret, error) {
	var secret corev1.Secret
	err := c.Get(ctx, key, &secret)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &secret, nil
}

func optionalConfigMapData(ctx context.Context, c client.Reader, key client.ObjectKey, field string) ([]byte, error) {
	var configMap corev1.ConfigMap
	err := c.Get(ctx, key, &configMap)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(configMap.Data[field]), nil
}

func secretData(secret *corev1.Secret, key string) []byte {
	if secret == nil {
		return nil
	}
	return secret.Data[key]
}

func deploymentExists(ctx context.Context, c client.Reader, key client.ObjectKey) (bool, error) {
	var deployment appsv1.Deployment
	err := c.Get(ctx, key, &deployment)
	if errors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
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

func (r *AddonReconciler) reconcileAddon(ctx context.Context, addon Addon, input AddonInput) (metav1.Condition, []metav1.Condition) {
	rendered, err := addon.Render(input)
	if err != nil {
		return addonCondition(addon, metav1.ConditionFalse, "RenderFailed", err.Error()), nil
	}
	condition, applied := r.applyRendered(ctx, addon, rendered)
	if !applied {
		return condition, rendered.Conditions
	}
	return condition, slices.Concat(rendered.Conditions, r.advance(ctx, addon, input))
}

func (r *AddonReconciler) applyRendered(ctx context.Context, addon Addon, rendered Rendered) (metav1.Condition, bool) {
	if rendered.SkipReason != "" {
		return addonCondition(addon, metav1.ConditionFalse, rendered.SkipReason, rendered.SkipMessage), false
	}
	for _, obj := range rendered.Objects {
		err := ssa.Apply(ctx, r.Client, obj, v1alpha1.OperatorFieldManager)
		if meta.IsNoMatchError(err) {
			return addonCondition(addon, metav1.ConditionFalse, "MissingCRD", fmt.Sprintf("%s %s: %v", obj.GetKind(), obj.GetName(), err)), false
		}
		if err != nil {
			return addonCondition(addon, metav1.ConditionFalse, "ApplyFailed", fmt.Sprintf("%s %s: %v", obj.GetKind(), obj.GetName(), err)), false
		}
	}
	for _, obj := range rendered.Absent {
		err := r.Client.Delete(ctx, obj)
		if err != nil && !errors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return addonCondition(addon, metav1.ConditionFalse, "ApplyFailed", fmt.Sprintf("%s %s: %v", obj.GetKind(), obj.GetName(), err)), false
		}
	}
	return r.probe(ctx, addon, rendered.Probes), true
}

func (r *AddonReconciler) advance(ctx context.Context, addon Addon, input AddonInput) []metav1.Condition {
	if addon.Advance == nil || r.Exec == nil {
		return nil
	}
	return addon.Advance(ctx, r.Client, r.Exec, input)
}

func (r *AddonReconciler) probe(ctx context.Context, addon Addon, probes []Probe) metav1.Condition {
	var pending []string
	var reason string
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
			if reason == "" {
				reason = readiness.Reason
			}
		}
	}
	if len(pending) > 0 {
		return addonCondition(addon, metav1.ConditionFalse, cmp.Or(reason, "Progressing"), strings.Join(pending, "; "))
	}
	return addonCondition(addon, metav1.ConditionTrue, "Ready", "")
}

func addonCondition(addon Addon, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: addon.Condition, Status: status, Reason: reason, Message: message}
}

func upgrading(status v1alpha1.ClusterStatus) bool {
	return status.Upgrade != nil && status.Upgrade.To != status.Version
}

func enqueueCluster() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: v1alpha1.ClusterName}}}
	})
}

func (r *AddonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("addons").
		For(&v1alpha1.Cluster{}).
		Watches(&v1alpha1.Host{}, enqueueCluster()).
		Watches(&v1alpha1.Setting{}, enqueueCluster()).
		Complete(r)
}
