package addons

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"
)

func TestAuditConfigMapFollowsTheSetting(t *testing.T) {
	metadata, err := AuditConfigMap("Metadata")
	if err != nil {
		t.Fatal(err)
	}
	full, err := AuditConfigMap("RequestResponse")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "bedrock-audit-policy" || metadata.Namespace != "bedrock-system" {
		t.Fatalf("object %s/%s", metadata.Namespace, metadata.Name)
	}
	a, b := strings.Split(metadata.Data["policy.yaml"], "\n"), strings.Split(full.Data["policy.yaml"], "\n")
	if len(a) != len(b) {
		t.Fatalf("policies differ in length: %d %d", len(a), len(b))
	}
	differing := 0
	for i := range a {
		if a[i] != b[i] {
			differing++
		}
	}
	if differing != 1 {
		t.Fatalf("the policies differ in %d lines, want the read rule only", differing)
	}
}

func TestAuditConfigMapRejectsAnInvalidLevel(t *testing.T) {
	if config, err := AuditConfigMap("Everything"); err == nil || config != nil {
		t.Fatalf("got %v %v", config, err)
	}
}

func alertmanagerConfig(t *testing.T, url string) map[string]any {
	t.Helper()
	secret, err := AlertmanagerSecret(url)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := sigyaml.Unmarshal(secret.Data["alertmanager.yaml"], &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func receiverNames(config map[string]any) []string {
	var names []string
	for _, item := range config["receivers"].([]any) {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	return names
}

func TestAlertmanagerSecretWithoutAWebhookRoutesToNull(t *testing.T) {
	config := alertmanagerConfig(t, "")
	if config["route"].(map[string]any)["receiver"] != "null" {
		t.Fatalf("route %v", config["route"])
	}
	if names := receiverNames(config); len(names) != 1 || names[0] != "null" {
		t.Fatalf("receivers %v", names)
	}
}

func TestAlertmanagerSecretWithAWebhook(t *testing.T) {
	config := alertmanagerConfig(t, "https://hooks.example.test/alert")
	route := config["route"].(map[string]any)
	if route["receiver"] != "bedrock-webhook" || route["group_wait"] != "30s" || route["group_interval"] != "5m" || route["repeat_interval"] != "12h" {
		t.Fatalf("route %v", route)
	}
	for _, item := range config["receivers"].([]any) {
		receiver := item.(map[string]any)
		if receiver["name"] != "bedrock-webhook" {
			continue
		}
		hook := receiver["webhook_configs"].([]any)[0].(map[string]any)
		if hook["url"] != "https://hooks.example.test/alert" || hook["send_resolved"] != true {
			t.Fatalf("hook %v", hook)
		}
		return
	}
	t.Fatal("no bedrock-webhook receiver")
}

func TestAlertmanagerSecretSilencesTheWatchdog(t *testing.T) {
	for _, url := range []string{"", "https://hooks.example.test/alert"} {
		routes := alertmanagerConfig(t, url)["route"].(map[string]any)["routes"].([]any)
		sub := routes[0].(map[string]any)
		matcher := sub["matchers"].([]any)[0].(string)
		if sub["receiver"] != "null" || !strings.Contains(matcher, "Watchdog") || !strings.Contains(matcher, "InfoInhibitor") {
			t.Errorf("url %q: sub-route %v", url, sub)
		}
	}
}

func newObservability(t *testing.T) (*ObservabilityReconciler, client.Client) {
	t.Helper()
	c := newClient(t)
	resetObservability(t, c)
	t.Cleanup(func() { resetObservability(t, c) })
	return &ObservabilityReconciler{Client: c, Random: rand.Reader}, c
}

func resetObservability(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	resetAddonsAndSettings(t, c)
	for _, obj := range []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "bedrock-grafana-admin"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "bedrock-alertmanager-config"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: "bedrock-audit-policy"}},
	} {
		_ = c.Delete(ctx, obj)
	}
}

func runObservability(t *testing.T, r *ObservabilityReconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "observability"}})
	return err
}

func grafanaPassword(t *testing.T, c client.Client) string {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "monitoring", Name: "bedrock-grafana-admin"}, &secret); err != nil {
		t.Fatal(err)
	}
	return string(secret.Data["admin-password"])
}

func TestGrafanaAdminSecretIsCreatedOnce(t *testing.T) {
	r, c := newObservability(t)
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	first := grafanaPassword(t, c)
	if len(first) != 24 {
		t.Fatalf("password length %d", len(first))
	}
	for _, char := range first {
		if !strings.ContainsRune(passwordAlphabet, char) {
			t.Fatalf("character %q outside the alphabet", char)
		}
	}
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	if grafanaPassword(t, c) != first {
		t.Fatal("the password changed")
	}
	if err := c.Delete(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "bedrock-grafana-admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	if second := grafanaPassword(t, c); second == first || len(second) != 24 {
		t.Fatalf("new password %q", second)
	}
}

func TestObservabilityAppliesAfterASettingChange(t *testing.T) {
	r, c := newObservability(t)
	ctx := context.Background()
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	setSetting(t, c, "audit.level", "RequestResponse")
	setSetting(t, c, "alertmanager.webhook-url", "http://hooks.example.test:9000/alerts")
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	var config corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "bedrock-audit-policy"}, &config); err != nil {
		t.Fatal(err)
	}
	expected, _ := AuditConfigMap("RequestResponse")
	if config.Data["policy.yaml"] != expected.Data["policy.yaml"] {
		t.Error("the audit policy did not follow the setting")
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "monitoring", Name: "bedrock-alertmanager-config"}, &secret); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secret.Data["alertmanager.yaml"]), "http://hooks.example.test:9000/alerts") {
		t.Error("the alertmanager secret did not follow the setting")
	}
}

type missingMonitoring struct{ client.Client }

type namespaced interface{ GetNamespace() *string }

func (m missingMonitoring) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	if ns, ok := obj.(namespaced); ok && ns.GetNamespace() != nil && *ns.GetNamespace() == "monitoring" {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "monitoring")
	}
	return m.Client.Apply(ctx, obj, opts...)
}

func (m missingMonitoring) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetNamespace() == "monitoring" {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "monitoring")
	}
	return m.Client.Create(ctx, obj, opts...)
}

func TestObservabilityDoesNotWaitForTheMonitoringNamespace(t *testing.T) {
	r, c := newObservability(t)
	r.Client = missingMonitoring{c}
	err := runObservability(t, r)
	if err == nil || !strings.Contains(err.Error(), "monitoring") {
		t.Fatalf("error %v", err)
	}
	var config corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: "bedrock-audit-policy"}, &config); err != nil {
		t.Fatalf("the audit ConfigMap is missing: %v", err)
	}
}
