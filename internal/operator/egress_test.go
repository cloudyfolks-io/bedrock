package operator

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func certManagerBundle() release.Bundle {
	controller := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"namespace": "cert-manager", "name": "cert-manager"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
			"name": "cert-manager-controller",
			"args": []any{"--v=2", "--dns01-recursive-nameservers=${BEDROCK_DNS01_NAMESERVERS}", "--dns01-recursive-nameservers-only=${BEDROCK_DNS01_ONLY}"},
			"env": []any{
				map[string]any{"name": "HTTP_PROXY", "value": "${BEDROCK_EGRESS_HTTP_PROXY}"},
				map[string]any{"name": "HTTPS_PROXY", "value": "${BEDROCK_EGRESS_HTTP_PROXY}"},
				map[string]any{"name": "NO_PROXY", "value": "${BEDROCK_EGRESS_NO_PROXY}"},
			},
		}}}}},
	}}
	return release.Bundle{Groups: []release.Group{{Name: "cert-manager", Objects: []*unstructured.Unstructured{controller}}}}
}

func controllerContainer(t *testing.T, rendered Rendered) (args []string, env map[string]string) {
	t.Helper()
	if len(rendered.Objects) != 1 {
		t.Fatalf("objects %d, skip %q", len(rendered.Objects), rendered.SkipReason)
	}
	containers, _, _ := unstructured.NestedSlice(rendered.Objects[0].Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	for _, arg := range container["args"].([]any) {
		args = append(args, arg.(string))
	}
	env = map[string]string{}
	for _, item := range container["env"].([]any) {
		entry := item.(map[string]any)
		value, _ := entry["value"].(string)
		env[entry["name"].(string)] = value
	}
	return args, env
}

func TestRenderEgressWithoutSettingsSetsNoProxyAndNoNameservers(t *testing.T) {
	rendered, err := RenderEgress(AddonInput{Bundle: certManagerBundle(), Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	args, env := controllerContainer(t, rendered)
	want := []string{"--v=2", "--dns01-recursive-nameservers=", "--dns01-recursive-nameservers-only=false"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args %v", args)
	}
	if env["HTTP_PROXY"] != "" || env["HTTPS_PROXY"] != "" || env["NO_PROXY"] != "" {
		t.Fatalf("env %v", env)
	}
}

func TestRenderEgressAppliesTheProxyAndTheNameservers(t *testing.T) {
	rendered, err := RenderEgress(AddonInput{Bundle: certManagerBundle(), Settings: map[string]string{
		"egress.http-proxy":             "http://proxy.kube-system:10808",
		"egress.no-proxy":               "10.0.0.0/8,.cloudyfolks.com,cluster.local,.svc",
		"letsencrypt.dns01-nameservers": "https://one.one.one.one/dns-query",
	}})
	if err != nil {
		t.Fatal(err)
	}
	args, env := controllerContainer(t, rendered)
	if !strings.Contains(strings.Join(args, " "), "--dns01-recursive-nameservers=https://one.one.one.one/dns-query --dns01-recursive-nameservers-only=true") {
		t.Fatalf("args %v", args)
	}
	if env["HTTP_PROXY"] != "http://proxy.kube-system:10808" || env["HTTPS_PROXY"] != "http://proxy.kube-system:10808" || env["NO_PROXY"] != "10.0.0.0/8,.cloudyfolks.com,cluster.local,.svc" {
		t.Fatalf("env %v", env)
	}
}

func TestRenderEgressRejectsAnInvalidProxy(t *testing.T) {
	if _, err := RenderEgress(AddonInput{Bundle: certManagerBundle(), Settings: map[string]string{"egress.http-proxy": "proxy:3128"}}); err == nil || !strings.Contains(err.Error(), "egress.http-proxy") {
		t.Fatalf("error %v", err)
	}
}

func TestRenderEgressWaitsForTheReleaseObject(t *testing.T) {
	rendered, err := RenderEgress(AddonInput{Settings: map[string]string{}})
	if err != nil || rendered.SkipReason != "CertManagerNotInRelease" {
		t.Fatalf("rendered %+v, error %v", rendered, err)
	}
}
