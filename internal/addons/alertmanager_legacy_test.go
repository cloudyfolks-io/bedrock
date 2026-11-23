package addons

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
