package operator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func decodeDocuments(t *testing.T, raw []byte) []*unstructured.Unstructured {
	t.Helper()
	var objects []*unstructured.Unstructured
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		obj := map[string]any{}
		if err := decoder.Decode(&obj); err != nil {
			break
		}
		if len(obj) > 0 {
			objects = append(objects, &unstructured.Unstructured{Object: obj})
		}
	}
	return objects
}

func corednsManifest(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	var objects []*unstructured.Unstructured
	files, err := filepath.Glob(filepath.Join("..", "..", "manifests", "22-coredns", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no coredns manifests: %v", err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, decodeDocuments(t, raw)...)
	}
	return objects
}

func findKind(t *testing.T, objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	t.Fatalf("no %s %s", kind, name)
	return nil
}

func TestCoreDNSReleaseRendersTheUpstreamsIntoTheCorefile(t *testing.T) {
	objects := corednsManifest(t)
	bundle := release.Bundle{Groups: []release.Group{{Name: "coredns", Objects: objects}}}
	rendered, err := RenderDNS(AddonInput{Bundle: bundle, Settings: map[string]string{"dns.upstreams": "217.218.127.127 217.218.155.155"}})
	if err != nil {
		t.Fatal(err)
	}
	corefile := corefileOf(t, rendered)
	if !strings.Contains(corefile, "forward . 217.218.127.127 217.218.155.155\n") || strings.Contains(corefile, "resolv.conf") || strings.Contains(corefile, "${") {
		t.Fatalf("corefile %q", corefile)
	}
	for _, plugin := range []string{"kubernetes cluster.local in-addr.arpa ip6.arpa", "cache 30", "loop", "reload", "ready", "health"} {
		if !strings.Contains(corefile, plugin) {
			t.Fatalf("corefile misses %q: %q", plugin, corefile)
		}
	}
}

func TestCoreDNSReleaseDefaultsToTheNodeResolvConf(t *testing.T) {
	bundle := release.Bundle{Groups: []release.Group{{Name: "coredns", Objects: corednsManifest(t)}}}
	rendered, err := RenderDNS(AddonInput{Bundle: bundle, Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if corefile := corefileOf(t, rendered); !strings.Contains(corefile, "forward . /etc/resolv.conf\n") {
		t.Fatalf("corefile %q", corefile)
	}
}

func TestCoreDNSReleaseServiceTakesTheClusterDNSAddress(t *testing.T) {
	objects := corednsManifest(t)
	service := findKind(t, objects, "Service", "kube-dns")
	if ip, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP"); ip != "${BEDROCK_DNS_IP}" {
		t.Fatalf("clusterIP %q", ip)
	}
	substituted, err := release.Substitute(release.Bundle{Groups: []release.Group{{Name: "coredns", Objects: objects}}}, map[string]string{release.VarDNSIP: "10.96.0.10", release.VarDNSForward: "/etc/resolv.conf"})
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range substituted.Groups[0].Objects {
		if obj.GetKind() == "Service" {
			if ip, _, _ := unstructured.NestedString(obj.Object, "spec", "clusterIP"); ip != "10.96.0.10" {
				t.Fatalf("clusterIP %q", ip)
			}
		}
	}
}

func TestCoreDNSReleaseDeploymentMountsTheCorefileOfTheConfigMap(t *testing.T) {
	objects := corednsManifest(t)
	deployment := findKind(t, objects, "Deployment", "coredns")
	volumes, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	if len(volumes) != 1 {
		t.Fatalf("volumes %v", volumes)
	}
	if name, _, _ := unstructured.NestedString(volumes[0].(map[string]any), "configMap", "name"); name != "coredns" {
		t.Fatalf("volume %v", volumes[0])
	}
	if policy, _, _ := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "dnsPolicy"); policy != "Default" {
		t.Fatalf("dnsPolicy %q", policy)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "replicas"); found {
		t.Fatal("the addon owns the replicas, one for each host up to two")
	}
}

func TestCoreDNSReleaseDeploymentSpreadsReplicasAndDropsRoot(t *testing.T) {
	deployment := findKind(t, corednsManifest(t), "Deployment", "coredns")
	terms, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "affinity", "podAntiAffinity", "preferredDuringSchedulingIgnoredDuringExecution")
	if len(terms) != 1 || fmt.Sprint(terms[0].(map[string]any)["weight"]) != "100" {
		t.Fatalf("anti-affinity %v", terms)
	}
	if key, _, _ := unstructured.NestedString(terms[0].(map[string]any), "podAffinityTerm", "topologyKey"); key != "kubernetes.io/hostname" {
		t.Fatalf("topology key %q", key)
	}
	if _, found, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution"); found {
		t.Fatal("a required rule keeps the surge pod of a roll off the only node")
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	if nonRoot, _, _ := unstructured.NestedBool(container, "securityContext", "runAsNonRoot"); !nonRoot {
		t.Fatalf("securityContext %v", container["securityContext"])
	}
	capabilities, _, _ := unstructured.NestedStringSlice(container, "securityContext", "capabilities", "add")
	if len(capabilities) != 1 || capabilities[0] != "NET_BIND_SERVICE" {
		t.Fatalf("capabilities %v", capabilities)
	}
	delay, _, _ := unstructured.NestedFieldNoCopy(container, "readinessProbe", "initialDelaySeconds")
	if fmt.Sprint(delay) != "30" {
		t.Fatalf("readiness delay %v", delay)
	}
}

func TestCoreDNSReleaseServiceCarriesTheScrapeAnnotations(t *testing.T) {
	service := findKind(t, corednsManifest(t), "Service", "kube-dns")
	if service.GetAnnotations()["prometheus.io/scrape"] != "true" || service.GetAnnotations()["prometheus.io/port"] != "9153" {
		t.Fatalf("annotations %v", service.GetAnnotations())
	}
}

func TestCoreDNSReleaseHasADisruptionBudgetNamedCoreDNS(t *testing.T) {
	budget := findKind(t, corednsManifest(t), "PodDisruptionBudget", "coredns")
	if budget.GetNamespace() != "kube-system" || budget.GetAPIVersion() != "policy/v1" {
		t.Fatalf("budget %v", budget.Object)
	}
	if limit, _, _ := unstructured.NestedFieldNoCopy(budget.Object, "spec", "maxUnavailable"); fmt.Sprint(limit) != "1" {
		t.Fatalf("maxUnavailable %v", limit)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(budget.Object, "spec", "minAvailable"); found {
		t.Fatal("one limit only")
	}
	if app, _, _ := unstructured.NestedString(budget.Object, "spec", "selector", "matchLabels", "k8s-app"); app != "kube-dns" {
		t.Fatalf("selector %v", budget.Object["spec"])
	}
}

func rollingUpdateLimits(t *testing.T, deployment *unstructured.Unstructured) (unavailable, surge string) {
	t.Helper()
	if kind, _, _ := unstructured.NestedString(deployment.Object, "spec", "strategy", "type"); kind != "RollingUpdate" {
		t.Fatalf("strategy %q", kind)
	}
	unavailableValue, _, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "strategy", "rollingUpdate", "maxUnavailable")
	surgeValue, _, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "strategy", "rollingUpdate", "maxSurge")
	return fmt.Sprint(unavailableValue), fmt.Sprint(surgeValue)
}

func TestCoreDNSReleaseKeepsAReadyPodDuringARollForOneAndTwoReplicas(t *testing.T) {
	deployment := findKind(t, corednsManifest(t), "Deployment", "coredns")
	unavailable, surge := rollingUpdateLimits(t, deployment)
	if unavailable != "0" || surge != "1" {
		t.Fatalf("maxUnavailable %s maxSurge %s", unavailable, surge)
	}
}
