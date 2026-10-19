package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/addons"
	"github.com/cloudyfolks-io/bedrock/internal/registry/harbor"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	ConditionConfigured = "HarborConfigured"
	adminUsername       = "admin"
	adminPasswordKey    = "HARBOR_ADMIN_PASSWORD"
	HarborURL           = "http://harbor-core." + Namespace + ".svc"
	coreDeployment      = "harbor-core"
	configuredRequeue   = 10 * time.Minute
	failedRequeue       = 30 * time.Second
)

type ProxyCache struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

type HarborConfig struct {
	RobotPrefix string
	GCCron      string
	Caches      []ProxyCache
}

func ParseHarborConfig(values map[string]string) (HarborConfig, error) {
	effective := map[string]string{}
	for _, key := range []string{"registry.robot-prefix", "registry.gc-cron", "registry.proxy-caches"} {
		def, _ := settings.Lookup(key)
		value := values[key]
		if value == "" {
			value = def.Default
		}
		if err := def.Validate(value); err != nil {
			return HarborConfig{}, err
		}
		effective[key] = value
	}
	config := HarborConfig{RobotPrefix: effective["registry.robot-prefix"], GCCron: effective["registry.gc-cron"]}
	if raw := effective["registry.proxy-caches"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.Caches); err != nil {
			return HarborConfig{}, err
		}
	}
	return config, nil
}

func Configuration(config HarborConfig) map[string]any {
	return map[string]any{
		"auth_mode":                    "db_auth",
		"self_registration":            false,
		"project_creation_restriction": "adminonly",
		"robot_name_prefix":            config.RobotPrefix,
	}
}

type HarborReconciler struct {
	Client  client.Client
	BaseURL string
	HTTP    *http.Client
}

func (r *HarborReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var addon v1alpha1.Addon
	err := r.Client.Get(ctx, client.ObjectKey{Name: addons.Registry}, &addon)
	if apierrors.IsNotFound(err) || (err == nil && !addon.Spec.Enabled) {
		return ctrl.Result{RequeueAfter: configuredRequeue}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	out, err := r.configure(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	status := metav1.ConditionFalse
	if out.ready {
		status = metav1.ConditionTrue
	}
	desired := metav1.Condition{Type: ConditionConfigured, Status: status, Reason: out.reason, Message: out.message, ObservedGeneration: addon.Generation}
	if err := setAddonCondition(ctx, r.Client, addon, desired); err != nil {
		logf.FromContext(ctx).Error(err, "write the Harbor configuration condition")
	}
	return ctrl.Result{RequeueAfter: out.requeue}, nil
}

func (r *HarborReconciler) configure(ctx context.Context) (result, error) {
	values, err := settings.Effective(ctx, r.Client)
	if err != nil {
		return result{}, err
	}
	config, err := ParseHarborConfig(values)
	if err != nil {
		return result{reason: "InvalidSettings", message: err.Error(), requeue: configuredRequeue}, nil
	}
	password, found, err := r.adminPassword(ctx)
	if err != nil {
		return result{}, err
	}
	if !found {
		return result{reason: "AdminSecretMissing", message: "the Secret harbor-admin in " + Namespace + " has no admin password", requeue: failedRequeue}, nil
	}
	harborClient := &harbor.Client{BaseURL: r.baseURL(), Username: adminUsername, Password: password, HTTP: r.HTTP}
	if err := harborClient.Health(ctx); err != nil {
		return result{reason: "HarborNotReady", message: err.Error(), requeue: failedRequeue}, nil
	}
	return applyConfiguration(ctx, harborClient, config), nil
}

func applyConfiguration(ctx context.Context, harborClient *harbor.Client, config HarborConfig) result {
	if err := harborClient.SetConfigurations(ctx, Configuration(config)); err != nil {
		return result{reason: "ConfigurationFailed", message: err.Error(), requeue: failedRequeue}
	}
	if err := harborClient.ScheduleGC(ctx, config.GCCron); err != nil {
		return result{reason: "GCScheduleFailed", message: err.Error(), requeue: failedRequeue}
	}
	if problem := ensureCaches(ctx, harborClient, config.Caches); problem != nil {
		return *problem
	}
	return result{ready: true, reason: "Configured", message: "Harbor has the configuration of the Settings", requeue: configuredRequeue}
}

func ensureCaches(ctx context.Context, harborClient *harbor.Client, caches []ProxyCache) *result {
	var mismatches []string
	for _, cache := range caches {
		id, err := harborClient.EnsureRegistryEndpoint(ctx, cache.Name, cache.Type, cache.URL)
		if err == nil {
			err = harborClient.EnsureProxyProject(ctx, cache.Name, id)
		}
		switch {
		case errors.Is(err, harbor.ErrTypeMismatch):
			mismatches = append(mismatches, err.Error())
		case err != nil:
			return &result{reason: "ProxyCacheFailed", message: fmt.Sprintf("cache %s: %v", cache.Name, err), requeue: failedRequeue}
		}
	}
	if len(mismatches) > 0 {
		return &result{reason: "ProxyCacheTypeMismatch", message: strings.Join(mismatches, "; "), requeue: configuredRequeue}
	}
	return nil
}

func (r *HarborReconciler) adminPassword(ctx context.Context) (string, bool, error) {
	var secret corev1.Secret
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: adminSecret}, &secret)
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	password := string(secret.Data[adminPasswordKey])
	return password, password != "", nil
}

func (r *HarborReconciler) baseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return HarborURL
}

func harborSetting(obj client.Object) bool {
	switch obj.GetName() {
	case "registry.robot-prefix", "registry.gc-cron", "registry.proxy-caches":
		return true
	}
	return false
}

func harborCore(obj client.Object) bool {
	return obj.GetNamespace() == Namespace && obj.GetName() == coreDeployment
}

func (r *HarborReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toRegistry := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: addons.Registry}}}
	})
	return ctrl.NewControllerManagedBy(mgr).Named("registry-harbor").
		For(&v1alpha1.Addon{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.Setting{}, toRegistry, builder.WithPredicates(predicate.NewPredicateFuncs(harborSetting))).
		Watches(&appsv1.Deployment{}, toRegistry, builder.WithPredicates(predicate.NewPredicateFuncs(harborCore))).
		Complete(r)
}
