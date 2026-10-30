package addons

import (
	"context"
	"errors"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/audit"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	monitoringNamespace = "monitoring"
	alertmanagerSecret  = "bedrock-alertmanager-config"
	alertmanagerKey     = "alertmanager.yaml"
	grafanaAdminSecret  = "bedrock-grafana-admin"
	webhookSetting      = "alertmanager.webhook-url"
	criticalSetting     = "alertmanager.critical-webhook-url"
	warningSetting      = "alertmanager.warning-webhook-url"
	heartbeatSetting    = "alertmanager.heartbeat-url"
	conditionWebhook    = "AlertmanagerWebhook"
	passwordLength      = 24
	passwordAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

var secretTypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}

func AuditConfigMap(level string) (*corev1.ConfigMap, error) {
	policy, err := audit.Policy(level)
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: release.SystemNamespace,
			Name:      audit.ConfigMapName,
			Labels:    map[string]string{v1alpha1.LabelKind: "Setting", v1alpha1.LabelName: "audit.level"},
		},
		Data: map[string]string{audit.PolicyFile: string(policy)},
	}, nil
}

type AlertmanagerTargets struct {
	Default   string
	Critical  string
	Warning   string
	Heartbeat string
}

func webhookReceiver(name, url string, resolved bool) map[string]any {
	return map[string]any{"name": name, "webhook_configs": []map[string]any{{"url": url, "send_resolved": resolved}}}
}

func alertmanagerRoutes(targets AlertmanagerTargets) []map[string]any {
	watchdog := map[string]any{"receiver": "null", "matchers": []string{`alertname = "Watchdog"`}}
	if targets.Heartbeat != "" {
		watchdog = map[string]any{"receiver": "bedrock-heartbeat", "matchers": []string{`alertname = "Watchdog"`}, "group_wait": "0s", "group_interval": "1m", "repeat_interval": "1m"}
	}
	routes := []map[string]any{watchdog, {"receiver": "null", "matchers": []string{`alertname = "InfoInhibitor"`}}}
	if targets.Critical != "" {
		routes = append(routes, map[string]any{"receiver": "bedrock-critical", "matchers": []string{`severity = "critical"`}})
	}
	if targets.Warning != "" {
		routes = append(routes, map[string]any{"receiver": "bedrock-warning", "matchers": []string{`severity = "warning"`}})
	}
	return routes
}

func AlertmanagerSecret(targets AlertmanagerTargets) (*corev1.Secret, error) {
	receiver := "null"
	receivers := []map[string]any{{"name": "null"}}
	if targets.Default != "" {
		receiver = "bedrock-webhook"
		receivers = append(receivers, webhookReceiver("bedrock-webhook", targets.Default, true))
	}
	if targets.Critical != "" {
		receivers = append(receivers, webhookReceiver("bedrock-critical", targets.Critical, true))
	}
	if targets.Warning != "" {
		receivers = append(receivers, webhookReceiver("bedrock-warning", targets.Warning, true))
	}
	if targets.Heartbeat != "" {
		receivers = append(receivers, webhookReceiver("bedrock-heartbeat", targets.Heartbeat, false))
	}
	config := map[string]any{
		"route": map[string]any{
			"receiver":        receiver,
			"group_by":        []string{"alertname", "namespace"},
			"group_wait":      "30s",
			"group_interval":  "5m",
			"repeat_interval": "12h",
			"routes":          alertmanagerRoutes(targets),
		},
		"receivers":     receivers,
		"inhibit_rules": []map[string]any{{"source_matchers": []string{`severity = "critical"`}, "target_matchers": []string{`severity = "warning"`}, "equal": []string{"alertname", "namespace"}}},
	}
	raw, err := sigyaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		TypeMeta: secretTypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Namespace: monitoringNamespace,
			Name:      alertmanagerSecret,
			Labels:    map[string]string{v1alpha1.LabelKind: "Setting", v1alpha1.LabelName: "alertmanager.webhook-url"},
		},
		Data: map[string][]byte{alertmanagerKey: raw},
	}, nil
}

func GrafanaAdminSecret(random io.Reader) (*corev1.Secret, error) {
	password, err := randomPassword(random)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		TypeMeta: secretTypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Namespace: monitoringNamespace,
			Name:      grafanaAdminSecret,
			Labels:    map[string]string{v1alpha1.LabelKind: "Addon", v1alpha1.LabelName: "grafana"},
		},
		Data: map[string][]byte{"admin-user": []byte("admin"), "admin-password": []byte(password)},
	}, nil
}

func randomPassword(random io.Reader) (string, error) {
	limit := byte(256 - 256%len(passwordAlphabet))
	password := make([]byte, 0, passwordLength)
	buffer := make([]byte, passwordLength)
	for len(password) < passwordLength {
		if _, err := io.ReadFull(random, buffer); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		for _, b := range buffer {
			if b < limit && len(password) < passwordLength {
				password = append(password, passwordAlphabet[int(b)%len(passwordAlphabet)])
			}
		}
	}
	return string(password), nil
}

type ObservabilityReconciler struct {
	Client client.Client
	Random io.Reader
}

func (r *ObservabilityReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if err := EnsureMonitoringNamespace(ctx, r.Client); err != nil {
		return ctrl.Result{}, err
	}
	values, err := settingValues(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	err = errors.Join(
		r.applyAudit(ctx, values["audit.level"]),
		r.applyAlertmanager(ctx, alertmanagerTargets(values)),
		r.ensureGrafanaAdmin(ctx),
	)
	return ctrl.Result{RequeueAfter: DefaultReadyInterval}, err
}

func (r *ObservabilityReconciler) applyAudit(ctx context.Context, level string) error {
	config, err := AuditConfigMap(level)
	if err != nil {
		return fmt.Errorf("audit policy: %w", err)
	}
	return ssa.Apply(ctx, r.Client, config, v1alpha1.AddonsFieldManager)
}

func validOrEmpty(key, value string) string {
	def, _ := settings.Lookup(key)
	if def.Validate(value) != nil {
		return ""
	}
	return value
}

func alertmanagerTargets(values map[string]string) AlertmanagerTargets {
	return AlertmanagerTargets{
		Default:   values[webhookSetting],
		Critical:  validOrEmpty(criticalSetting, values[criticalSetting]),
		Warning:   validOrEmpty(warningSetting, values[warningSetting]),
		Heartbeat: validOrEmpty(heartbeatSetting, values[heartbeatSetting]),
	}
}

func (r *ObservabilityReconciler) applyAlertmanager(ctx context.Context, targets AlertmanagerTargets) error {
	if err := validWebhookURL(targets.Default); err != nil {
		r.reportWebhook(ctx, metav1.ConditionFalse, "InvalidURL", err.Error())
		return r.ensureAlertmanagerConfig(ctx)
	}
	secret, err := AlertmanagerSecret(targets)
	if err != nil {
		return fmt.Errorf("alertmanager config: %w", err)
	}
	if err := ssa.Apply(ctx, r.Client, secret, v1alpha1.AddonsFieldManager); err != nil {
		return fmt.Errorf("alertmanager config in namespace %s: %w", monitoringNamespace, err)
	}
	r.reportWebhook(ctx, metav1.ConditionTrue, "Applied", "the webhook is in the Alertmanager configuration")
	return nil
}

func (r *ObservabilityReconciler) ensureAlertmanagerConfig(ctx context.Context) error {
	var existing corev1.Secret
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: monitoringNamespace, Name: alertmanagerSecret}, &existing)
	if err == nil || !apierrors.IsNotFound(err) {
		return err
	}
	secret, err := AlertmanagerSecret(AlertmanagerTargets{})
	if err != nil {
		return fmt.Errorf("alertmanager config: %w", err)
	}
	if err := ssa.Apply(ctx, r.Client, secret, v1alpha1.AddonsFieldManager); err != nil {
		return fmt.Errorf("alertmanager config in namespace %s: %w", monitoringNamespace, err)
	}
	return nil
}

func validWebhookURL(webhookURL string) error {
	def, _ := settings.Lookup(webhookSetting)
	return def.Validate(webhookURL)
}

func (r *ObservabilityReconciler) reportWebhook(ctx context.Context, status metav1.ConditionStatus, reason, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var setting v1alpha1.Setting
		if err := r.Client.Get(ctx, client.ObjectKey{Name: webhookSetting}, &setting); err != nil {
			return client.IgnoreNotFound(err)
		}
		v1alpha1.SetCondition(&setting.Status.Conditions, metav1.Condition{Type: conditionWebhook, Status: status, Reason: reason, Message: message, ObservedGeneration: setting.Generation})
		return r.Client.Status().Update(ctx, &setting, client.FieldOwner(v1alpha1.AddonsFieldManager))
	})
	if err != nil {
		log.FromContext(ctx).Error(err, "write webhook condition")
	}
}

func (r *ObservabilityReconciler) ensureGrafanaAdmin(ctx context.Context) error {
	return EnsureGrafanaAdmin(ctx, r.Client, r.Random)
}

func EnsureMonitoringNamespace(ctx context.Context, c client.Client) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: monitoringNamespace}}
	return client.IgnoreAlreadyExists(c.Create(ctx, namespace, client.FieldOwner(v1alpha1.AddonsFieldManager)))
}

func EnsureGrafanaAdmin(ctx context.Context, c client.Client, random io.Reader) error {
	var existing corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Namespace: monitoringNamespace, Name: grafanaAdminSecret}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	secret, err := GrafanaAdminSecret(random)
	if err != nil {
		return err
	}
	if err := c.Create(ctx, secret, client.FieldOwner(v1alpha1.AddonsFieldManager)); client.IgnoreAlreadyExists(err) != nil {
		return fmt.Errorf("grafana admin secret in namespace %s: %w", monitoringNamespace, err)
	}
	return nil
}

func (r *ObservabilityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	oneRequest := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "observability"}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("addon-observability").
		Watches(&v1alpha1.Setting{}, oneRequest).
		Complete(r)
}
