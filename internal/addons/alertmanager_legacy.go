package addons

import (
	"errors"
	"fmt"
)

func validateLegacyAlertmanagerTargets(targets AlertmanagerTargets) error {
	if targets.Default != "" {
		return errors.New("clear the default webhook before selecting the legacy routing profile")
	}
	for _, setting := range []struct{ name, value string }{
		{warningSetting, targets.Warning}, {criticalSetting, targets.Critical},
		{infoSetting, targets.Info}, {heartbeatSetting, targets.Heartbeat},
	} {
		if setting.value == "" || validWebhookURL(setting.value) != nil {
			return fmt.Errorf("the legacy routing profile requires a valid %s", setting.name)
		}
	}
	if err := validWebhookURL(targets.HeartbeatProxy); err != nil {
		return fmt.Errorf("invalid %s: %w", heartbeatProxySetting, err)
	}
	return nil
}

func legacyAlertmanagerConfig(targets AlertmanagerTargets) map[string]any {
	heartbeat := map[string]any{"url": targets.Heartbeat, "send_resolved": false, "timeout": "10s"}
	if targets.HeartbeatProxy != "" {
		heartbeat["http_config"] = map[string]any{"proxy_url": targets.HeartbeatProxy}
	}
	equal := []string{"environment", "namespace", "alertname"}
	return map[string]any{
		"route": map[string]any{
			"receiver": "warning", "group_by": []string{"environment", "alertname", "namespace"},
			"group_wait": "30s", "group_interval": "5m", "repeat_interval": "24h",
			"routes": []map[string]any{
				{"matchers": []string{`alertname = "Watchdog"`}, "receiver": "heartbeat", "group_wait": "0s", "group_interval": "1m", "repeat_interval": "1m"},
				{"matchers": []string{`severity = "critical"`}, "receiver": "critical", "repeat_interval": "4h"},
				{"matchers": []string{`severity = "info"`}, "receiver": "info", "repeat_interval": "7d"},
			},
		},
		"inhibit_rules": []map[string]any{
			{"source_matchers": []string{`severity = "critical"`}, "target_matchers": []string{`severity =~ "warning|info"`}, "equal": equal},
			{"source_matchers": []string{`severity = "warning"`}, "target_matchers": []string{`severity = "info"`}, "equal": equal},
		},
		"receivers": []map[string]any{
			{"name": "critical", "webhook_configs": []map[string]any{{"url": targets.Critical, "timeout": "30s"}}},
			{"name": "warning", "webhook_configs": []map[string]any{{"url": targets.Warning, "timeout": "30s"}}},
			{"name": "info", "webhook_configs": []map[string]any{{"url": targets.Info, "timeout": "30s"}}},
			{"name": "heartbeat", "webhook_configs": []map[string]any{heartbeat}},
		},
	}
}
