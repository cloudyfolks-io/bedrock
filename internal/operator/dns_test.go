package operator

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const testCorefile = ".:53 {\n    forward . ${BEDROCK_DNS_FORWARD}\n    cache 30\n}\n"

func dnsBundle() release.Bundle {
	config := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"namespace": "kube-system", "name": "coredns"},
		"data":     map[string]any{"Corefile": testCorefile},
	}}
	return release.Bundle{Groups: []release.Group{{Name: "coredns", Objects: []*unstructured.Unstructured{config}}}}
}

func corefileOf(t *testing.T, rendered Rendered) string {
	t.Helper()
	for _, obj := range rendered.Objects {
		if obj.GetKind() == "ConfigMap" {
			text, _, _ := unstructured.NestedString(obj.Object, "data", "Corefile")
			return text
		}
	}
	t.Fatalf("no ConfigMap in %d objects, skip %q", len(rendered.Objects), rendered.SkipReason)
	return ""
}

func replicasOf(t *testing.T, hosts int) any {
	t.Helper()
	list := make([]v1alpha1.Host, hosts)
	rendered, err := RenderDNS(AddonInput{Bundle: dnsBundle(), Hosts: list, Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range rendered.Objects {
		if obj.GetKind() == "Deployment" && obj.GetName() == "coredns" && obj.GetNamespace() == "kube-system" {
			replicas, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "replicas")
			return replicas
		}
	}
	t.Fatalf("no Deployment in %d objects", len(rendered.Objects))
	return nil
}

func TestRenderDNSRunsOneReplicaForEachHostUpToTwo(t *testing.T) {
	cases := map[int]int64{0: 1, 1: 1, 2: 2, 3: 2, 9: 2}
	for hosts, want := range cases {
		if got := replicasOf(t, hosts); got != want {
			t.Errorf("%d hosts: replicas %v, want %d", hosts, got, want)
		}
	}
}

func TestRenderDNSSetsOnlyTheReplicasOfTheDeployment(t *testing.T) {
	rendered, err := RenderDNS(AddonInput{Bundle: dnsBundle(), Hosts: make([]v1alpha1.Host, 2), Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range rendered.Objects {
		if obj.GetKind() == "Deployment" {
			if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "template"); found || len(obj.Object["spec"].(map[string]any)) != 1 {
				t.Fatalf("the release owns every other field: %v", obj.Object)
			}
		}
	}
}

func TestRenderDNSForwardsToTheNodeResolvConfByDefault(t *testing.T) {
	rendered, err := RenderDNS(AddonInput{Bundle: dnsBundle(), Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := corefileOf(t, rendered); !strings.Contains(got, "forward . /etc/resolv.conf\n") {
		t.Fatalf("corefile %q", got)
	}
}

func TestRenderDNSForwardsToExactlyTheUpstreams(t *testing.T) {
	rendered, err := RenderDNS(AddonInput{Bundle: dnsBundle(), Settings: map[string]string{"dns.upstreams": "217.218.127.127 217.218.155.155"}})
	if err != nil {
		t.Fatal(err)
	}
	got := corefileOf(t, rendered)
	if !strings.Contains(got, "forward . 217.218.127.127 217.218.155.155\n") || strings.Contains(got, "resolv.conf") || strings.Contains(got, "${") {
		t.Fatalf("corefile %q", got)
	}
}

func TestRenderDNSLeavesTheBundleUntouched(t *testing.T) {
	bundle := dnsBundle()
	if _, err := RenderDNS(AddonInput{Bundle: bundle, Settings: map[string]string{"dns.upstreams": "9.9.9.9"}}); err != nil {
		t.Fatal(err)
	}
	if text, _, _ := unstructured.NestedString(bundle.Groups[0].Objects[0].Object, "data", "Corefile"); text != testCorefile {
		t.Fatalf("bundle corefile changed: %q", text)
	}
}

func TestRenderDNSRejectsAnInvalidUpstream(t *testing.T) {
	if _, err := RenderDNS(AddonInput{Bundle: dnsBundle(), Settings: map[string]string{"dns.upstreams": "dns.example.com"}}); err == nil || !strings.Contains(err.Error(), "dns.upstreams") {
		t.Fatalf("error %v", err)
	}
}

func TestRenderDNSProbesTheCoreDNSDeployment(t *testing.T) {
	rendered, err := RenderDNS(AddonInput{Bundle: dnsBundle(), Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Probes) != 1 || rendered.Probes[0].GVK != deploymentGVK || rendered.Probes[0].Key.Namespace != "kube-system" || rendered.Probes[0].Key.Name != "coredns" {
		t.Fatalf("probes %+v", rendered.Probes)
	}
}

func TestRenderDNSWaitsForTheReleaseObject(t *testing.T) {
	rendered, err := RenderDNS(AddonInput{Settings: map[string]string{}})
	if err != nil || rendered.SkipReason != "DNSNotInRelease" {
		t.Fatalf("rendered %+v, error %v", rendered, err)
	}
}
