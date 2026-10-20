package database

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

var (
	genericAlertsFile    = filepath.Join("..", "..", "manifests", "addons", "cnpg", "database-alerts.yaml")
	postgresqlAlertsFile = filepath.Join("..", "..", "manifests", "addons", "cnpg", "alerts.yaml")
	metricIdentifier     = regexp.MustCompile(`\b(?:kubelet_|kube_|redis_|cnpg_)[a-z0-9_]+`)
	knownMetrics         = []string{
		"kubelet_volume_stats_used_bytes", "kubelet_volume_stats_capacity_bytes", "kube_pod_spec_volumes_persistentvolumeclaims_info",
		"kube_pod_labels", "kube_pod_status_ready", "kube_pod_status_phase", "kube_pod_container_status_restarts_total",
		"redis_memory_used_bytes", "redis_memory_max_bytes",
		"cnpg_pg_replication_lag", "cnpg_collector_last_available_backup_timestamp", "cnpg_pg_stat_archiver_failed_count",
	}
)

type alert struct {
	name        string
	expr        string
	duration    string
	severity    string
	description string
}

func rulesOf(t *testing.T, path string) (*unstructured.Unstructured, []alert) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object unstructured.Unstructured
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&object); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	groups, _, _ := unstructured.NestedSlice(object.Object, "spec", "groups")
	var alerts []alert
	for _, group := range groups {
		rules, _ := group.(map[string]any)["rules"].([]any)
		for _, item := range rules {
			rule := item.(map[string]any)
			labels, _ := rule["labels"].(map[string]any)
			annotations, _ := rule["annotations"].(map[string]any)
			alerts = append(alerts, alert{
				name: rule["alert"].(string), expr: rule["expr"].(string), duration: asString(rule["for"]),
				severity: asString(labels["severity"]), description: asString(annotations["description"]),
			})
		}
	}
	return &object, alerts
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func allAlerts(t *testing.T) []alert {
	t.Helper()
	_, generic := rulesOf(t, genericAlertsFile)
	_, postgresql := rulesOf(t, postgresqlAlertsFile)
	return slices.Concat(generic, postgresql)
}

func labelKey(label string) string {
	return "label_" + strings.NewReplacer(".", "_", "/", "_", "-", "_").Replace(label)
}

func TestAlertRulesAreMonitoringObjectsInTheMonitoringNamespace(t *testing.T) {
	for _, path := range []string{genericAlertsFile, postgresqlAlertsFile} {
		object, alerts := rulesOf(t, path)
		if object.GetKind() != "PrometheusRule" || object.GetAPIVersion() != "monitoring.coreos.com/v1" || object.GetNamespace() != "monitoring" || len(alerts) == 0 {
			t.Errorf("%s: %s %s in %q with %d alerts", path, object.GetAPIVersion(), object.GetKind(), object.GetNamespace(), len(alerts))
		}
	}
}

func TestTheEightDadehatAlertsAreKept(t *testing.T) {
	want := map[string][3]string{
		"DbaasDiskNearlyFull":    {"10m", "warning", ""},
		"DbaasDiskCritical":      {"5m", "critical", ""},
		"DbaasPodNotReady":       {"10m", "critical", ""},
		"DbaasPodRestarting":     {"5m", "warning", ""},
		"DbaasReplicationLag":    {"5m", "warning", ""},
		"DbaasBackupStale":       {"30m", "warning", ""},
		"DbaasWalArchiveFailing": {"", "critical", ""},
		"DbaasValkeyMemoryHigh":  {"10m", "warning", ""},
	}
	got := allAlerts(t)
	if len(got) != len(want) {
		t.Fatalf("%d alerts, want %d", len(got), len(want))
	}
	for _, a := range got {
		expected, ok := want[a.name]
		if !ok || a.duration != expected[0] || a.severity != expected[1] {
			t.Errorf("alert %s: for %q severity %q", a.name, a.duration, a.severity)
		}
		delete(want, a.name)
	}
	if len(want) != 0 {
		t.Errorf("missing alerts %v", want)
	}
}

func TestPostgreSQLAlertsShipWithTheCNPGAddonAndTheRestWithTheBase(t *testing.T) {
	_, postgresql := rulesOf(t, postgresqlAlertsFile)
	_, generic := rulesOf(t, genericAlertsFile)
	names := func(alerts []alert) []string {
		var out []string
		for _, a := range alerts {
			out = append(out, a.name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(postgresql); !slices.Equal(got, []string{"DbaasBackupStale", "DbaasReplicationLag", "DbaasWalArchiveFailing"}) {
		t.Errorf("cnpg alerts %v", got)
	}
	if got := names(generic); !slices.Equal(got, []string{"DbaasDiskCritical", "DbaasDiskNearlyFull", "DbaasPodNotReady", "DbaasPodRestarting", "DbaasValkeyMemoryHigh"}) {
		t.Errorf("base alerts %v", got)
	}
	for _, a := range postgresql {
		if !strings.Contains(a.expr, "cnpg_") {
			t.Errorf("%s reads no cnpg series", a.name)
		}
	}
}

func TestAlertsSelectTheDatabasePlatformNamespaceOnly(t *testing.T) {
	for _, a := range allAlerts(t) {
		if !strings.Contains(a.expr, `namespace="`+PlatformNamespace+`"`) {
			t.Errorf("%s does not select the namespace %s", a.name, PlatformNamespace)
		}
		for _, forbidden := range []string{`namespace=~`, `t-.+`, "database-api"} {
			if strings.Contains(a.expr, forbidden) {
				t.Errorf("%s still holds %s", a.name, forbidden)
			}
		}
	}
}

func TestAlertsJoinTheLabelsThatTheRenderersSet(t *testing.T) {
	managedBy, instance, name := labelKey(LabelManagedBy), labelKey(LabelInstance), labelKey(v1alpha1.LabelName)
	for _, a := range allAlerts(t) {
		for _, want := range []string{
			"kube_pod_labels{", managedBy + `=~"bedrock|percona-server-mysql-operator"`,
			"group_left(" + instance + ", " + name + ")",
		} {
			if !strings.Contains(a.expr, want) {
				t.Errorf("%s lacks %s", a.name, want)
			}
		}
		for _, want := range []string{"{{ $labels." + name + " }}", "{{ $labels." + instance + " }}"} {
			if !strings.Contains(a.description, want) {
				t.Errorf("%s: the description does not name the database with %s", a.name, want)
			}
		}
	}
}

func rendererPodLabels(t *testing.T) map[string]map[string]string {
	t.Helper()
	labels := map[string]string{
		v1alpha1.LabelKind: Kind, v1alpha1.LabelName: "orders", LabelManagedBy: ManagedBy, LabelInstance: "uid-1",
	}
	podLabels := map[string]string{LabelComponent: ComponentEngine}
	for key, value := range labels {
		podLabels[key] = value
	}
	spec := engine.Spec{
		Name: "uid-1", Namespace: PlatformNamespace, Image: "engine@sha256:aaa", CPUMilli: 500, MemoryMi: 1024, StorageGi: 10, Instances: 1,
		Labels: labels, PodLabels: podLabels, Images: map[string]string{valkey.ImageExporter: "exporter@sha256:bbb"},
	}
	out := map[string]map[string]string{}
	valkeyObjects, err := valkey.Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range valkeyObjects {
		if obj.GetKind() == "Deployment" {
			out["valkey"], _, _ = unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "labels")
		}
	}
	mysqlObjects, err := mysql.Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	out["mysql"], _, _ = unstructured.NestedStringMap(mysqlObjects[0].Object, "spec", "mysql", "labels")
	return out
}

func TestRendererPodLabelsCarryTheInstanceAndTheDatabaseName(t *testing.T) {
	for engineName, labels := range rendererPodLabels(t) {
		if labels[LabelInstance] != "uid-1" || labels[v1alpha1.LabelName] != "orders" {
			t.Errorf("%s: the pod labels %v do not name the database", engineName, labels)
		}
	}
	valkeyLabels := rendererPodLabels(t)["valkey"]
	if !regexp.MustCompile(`^(bedrock|percona-server-mysql-operator)$`).MatchString(valkeyLabels[LabelManagedBy]) {
		t.Errorf("the managed-by value %q does not match the alert join", valkeyLabels[LabelManagedBy])
	}
}

func TestAlertsUseKnownSeriesAndBalancedExpressions(t *testing.T) {
	for _, a := range allAlerts(t) {
		for _, metric := range metricIdentifier.FindAllString(a.expr, -1) {
			if !slices.Contains(knownMetrics, metric) {
				t.Errorf("%s uses the unknown series %s", a.name, metric)
			}
		}
		for _, pair := range [][2]string{{"(", ")"}, {"{", "}"}, {"[", "]"}} {
			if strings.Count(a.expr, pair[0]) != strings.Count(a.expr, pair[1]) {
				t.Errorf("%s: unbalanced %s%s", a.name, pair[0], pair[1])
			}
		}
		if strings.Count(a.expr, `"`)%2 != 0 {
			t.Errorf("%s: unbalanced quotes", a.name)
		}
	}
}

func TestEveryAlertNeedsARunningPodSoASuspendedDatabaseStaysQuiet(t *testing.T) {
	for _, a := range allAlerts(t) {
		if !strings.Contains(a.expr, "kube_pod_labels{") {
			t.Errorf("%s does not join the labels of a live pod", a.name)
		}
	}
}

func TestTheValkeyMemoryAlertReadsTheExporterSeries(t *testing.T) {
	for _, a := range allAlerts(t) {
		if a.name == "DbaasValkeyMemoryHigh" && (!strings.Contains(a.expr, "redis_memory_used_bytes") || !strings.Contains(a.expr, "redis_memory_max_bytes")) {
			t.Errorf("expr %s", a.expr)
		}
	}
}

func TestAlertFilesHaveNoComments(t *testing.T) {
	for _, path := range []string{genericAlertsFile, postgresqlAlertsFile} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				t.Errorf("%s holds a comment: %s", path, line)
			}
		}
	}
}
