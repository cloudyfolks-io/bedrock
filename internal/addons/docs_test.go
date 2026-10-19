package addons

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/audit"
)

func TestAddonsDocNamesEveryContractName(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "addons.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	required := []string{
		"kata", "loki",
		"alertmanager.webhook-url", "audit.level", "kata.enabled", "loki.enabled", "loki.retention-days",
		"bedrock:project_vm_running:count", "bedrock:project_vm_vcpu:sum", "bedrock:project_vm_memory_bytes:sum",
		"bedrock:project_volume_requested_bytes:sum", "bedrock:project_vm_cpu_usage_cores:rate5m",
		"bedrock:project_vm_network_receive_bytes:rate5m", "bedrock:project_vm_network_transmit_bytes:rate5m",
		"bedrock:project_vm_network_receive_bytes:increase1h", "bedrock:project_vm_network_transmit_bytes:increase1h",
		"Ready", "Progressing", "Disabled", "PreflightFailed", "PreflightPending", "InvalidValues", "UnknownAddon",
		"WaitingForCluster", "ApplyFailed", "MissingCRD", "RenderFailed", "Pruning",
		"Passed", "NoKVM", "WaitingForHosts", ConditionPreflight,
		audit.Dir + "/" + audit.PolicyFile, audit.LogDir + "/" + audit.LogFile,
		"monitoring/bedrock-alertmanager-config", "bedrock-grafana-admin", audit.ConfigMapName, LabelKVM,
		"bedrock-addon-<name>-inventory", "kube-prometheus-stack-prometheus", "bedrock-vms", "bedrock-tenants",
	}
	for _, def := range Known() {
		required = append(required, def.Name, def.EnabledSetting)
		for _, value := range def.Values {
			required = append(required, value.Key, value.Default)
		}
	}
	for _, name := range required {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/addons.md does not mention %q", name)
		}
	}
}
