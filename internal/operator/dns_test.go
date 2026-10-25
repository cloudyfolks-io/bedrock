package operator

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
	if len(rendered.Objects) != 1 {
		t.Fatalf("objects %d, skip %q", len(rendered.Objects), rendered.SkipReason)
	}
	text, _, _ := unstructured.NestedString(rendered.Objects[0].Object, "data", "Corefile")
	return text
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
