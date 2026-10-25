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
	if replicas, _, _ := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "replicas"); fmt.Sprint(replicas) != "2" {
		t.Fatalf("replicas %v", replicas)
	}
}
