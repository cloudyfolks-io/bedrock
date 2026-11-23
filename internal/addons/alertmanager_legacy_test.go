package addons

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestAlertmanagerLegacyRoutesMatchTheSource(t *testing.T) {
	config, receivers := alertmanagerFor(t, AlertmanagerTargets{
		RoutingProfile: "legacy", Warning: "https://hooks.example.test/warning", Critical: "https://hooks.example.test/critical",
		Info: "https://hooks.example.test/info", Heartbeat: "https://heartbeat.example.test/watchdog", HeartbeatProxy: "http://proxy.example.test:8080",
	})
	var expected map[string]any
	if err := sigyaml.Unmarshal([]byte(`route:
  receiver: warning
  group_by: [environment, alertname, namespace]
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 24h
  routes:
    - matchers: ['alertname = "Watchdog"']
      receiver: heartbeat
      group_wait: 0s
      group_interval: 1m
      repeat_interval: 1m
    - matchers: ['severity = "critical"']
      receiver: critical
      repeat_interval: 4h
    - matchers: ['severity = "info"']
      receiver: info
      repeat_interval: 7d
inhibit_rules:
  - source_matchers: ['severity = "critical"']
    target_matchers: ['severity =~ "warning|info"']
    equal: [environment, namespace, alertname]
  - source_matchers: ['severity = "warning"']
    target_matchers: ['severity = "info"']
    equal: [environment, namespace, alertname]
receivers:
  - name: critical
    webhook_configs:
      - url: https://hooks.example.test/critical
        timeout: 30s
  - name: warning
    webhook_configs:
      - url: https://hooks.example.test/warning
        timeout: 30s
  - name: info
    webhook_configs:
      - url: https://hooks.example.test/info
        timeout: 30s
  - name: heartbeat
    webhook_configs:
      - url: https://heartbeat.example.test/watchdog
        timeout: 10s
        send_resolved: false
        http_config:
          proxy_url: http://proxy.example.test:8080
`), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config, expected) {
		t.Fatalf("legacy route structure differs: %v", config)
	}
	if len(receivers) != 4 {
		t.Fatalf("got %d legacy receivers", len(receivers))
	}
}

type applyWithRoutingChange struct {
	client.Client
	change func(context.Context) error
}

func (c applyWithRoutingChange) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	if err := c.Client.Apply(ctx, obj, opts...); err != nil {
		return err
	}
	return c.change(ctx)
}

func TestAlertmanagerStatusDoesNotCertifyChangedRoutingSettings(t *testing.T) {
	for _, name := range []string{infoSetting, routingProfileSetting} {
		t.Run(name, func(t *testing.T) {
			r, c := newObservability(t)
			for name, value := range map[string]string{
				routingProfileSetting: "legacy", warningSetting: "https://example.test/warning", criticalSetting: "https://example.test/critical",
				infoSetting: "https://example.test/info", heartbeatSetting: "https://example.test/ping",
			} {
				setSetting(t, c, name, value)
			}
			ctx := context.Background()
			var list v1alpha1.SettingList
			if err := c.List(ctx, &list); err != nil {
				t.Fatal(err)
			}
			previous := routingSettingsRevisions(list.Items)
			r.Client = applyWithRoutingChange{Client: c, change: func(ctx context.Context) error {
				var setting v1alpha1.Setting
				if err := c.Get(ctx, client.ObjectKey{Name: name}, &setting); err != nil {
					return err
				}
				if name == routingProfileSetting {
					if err := c.Delete(ctx, &setting); err != nil {
						return err
					}
					return c.Create(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: setting.Spec})
				}
				setting.Spec.Value = "https://example.test/changed"
				return c.Update(ctx, &setting)
			}}
			err := r.applyAlertmanager(ctx, alertmanagerTargets(listedSettingValues(list.Items)), previous)
			if err == nil || !strings.Contains(err.Error(), "settings changed during reconciliation") {
				t.Fatalf("expected a reconciliation retry, got %v", err)
			}
			var profile v1alpha1.Setting
			if err := c.Get(ctx, client.ObjectKey{Name: routingProfileSetting}, &profile); err != nil {
				t.Fatal(err)
			}
			if meta.FindStatusCondition(profile.Status.Conditions, conditionWebhook) != nil {
				t.Fatal("the stale settings received an Alertmanager condition")
			}
			r.Client = c
			if err := runObservability(t, r); err != nil {
				t.Fatal(err)
			}
			if err := c.List(ctx, &list); err != nil {
				t.Fatal(err)
			}
			digest := routingInputsDigest(routingSettingsRevisions(list.Items))
			if digest == routingInputsDigest(previous) {
				t.Fatal("the input digest did not change")
			}
			var secret corev1.Secret
			if err := c.Get(ctx, client.ObjectKey{Namespace: monitoringNamespace, Name: alertmanagerSecret}, &secret); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, client.ObjectKey{Name: routingProfileSetting}, &profile); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(profile.Status.Conditions, conditionWebhook)
			if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != profile.Generation ||
				!strings.HasSuffix(condition.Message, digest) || secret.Annotations[alertmanagerInputsDigest] != digest {
				t.Fatal("the Secret and condition do not certify the new snapshot")
			}
		})
	}
}

func TestAlertmanagerLegacyProxyRequiresAnExplicitSetting(t *testing.T) {
	_, receivers := alertmanagerFor(t, AlertmanagerTargets{RoutingProfile: "legacy", Warning: "https://example.test/warning",
		Critical: "https://example.test/critical", Info: "https://example.test/info", Heartbeat: "https://example.test/ping"})
	hook := receivers["heartbeat"]["webhook_configs"].([]any)[0].(map[string]any)
	if _, exists := hook["http_config"]; exists {
		t.Fatal("legacy heartbeat has an implicit proxy")
	}
	standard, receivers := alertmanagerFor(t, AlertmanagerTargets{RoutingProfile: "bedrock", Info: "https://example.test/info",
		Heartbeat: "https://example.test/ping", HeartbeatProxy: "https://example.test/proxy"})
	if routeFor(standard, `severity = "info"`) != nil || len(receivers) != 2 {
		t.Fatal("legacy settings changed the standard routing profile")
	}
	hook = receivers["bedrock-heartbeat"]["webhook_configs"].([]any)[0].(map[string]any)
	if _, exists := hook["http_config"]; exists {
		t.Fatal("legacy proxy changed the standard heartbeat")
	}
}

func TestObservabilityDisablesLegacyRoutingWhenARequiredURLDisappears(t *testing.T) {
	r, c := newObservability(t)
	for name, value := range map[string]string{
		routingProfileSetting: "legacy", warningSetting: "https://example.test/warning", criticalSetting: "https://example.test/critical",
		infoSetting: "https://example.test/info", heartbeatSetting: "https://example.test/ping",
	} {
		setSetting(t, c, name, value)
	}
	ctx := context.Background()
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: monitoringNamespace, Name: alertmanagerSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secret.Data[alertmanagerKey]), "https://example.test/info") {
		t.Fatal("the legacy route was not applied")
	}
	var info v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: infoSetting}, &info); err != nil {
		t.Fatal(err)
	}
	info.Spec.Value = ""
	if err := c.Update(ctx, &info); err != nil {
		t.Fatal(err)
	}
	if err := runObservability(t, r); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: monitoringNamespace, Name: alertmanagerSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	if config := string(secret.Data[alertmanagerKey]); strings.Contains(config, "https://example.test/") || !strings.Contains(config, `receiver: "null"`) {
		t.Fatal("the incomplete legacy profile still delivers notifications")
	}
}
