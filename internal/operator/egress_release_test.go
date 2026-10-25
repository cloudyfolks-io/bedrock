package operator

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestCertManagerChartTakesTheEgressSettings(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	cmd := exec.Command("helm", "template", "cert-manager", "charts/cert-manager", "--namespace", "cert-manager", "-f", "release/values/cert-manager.yaml")
	cmd.Dir = filepath.Join("..", "..")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template: %v: %s", err, stderr.String())
	}
	bundle := release.Bundle{Groups: []release.Group{{Name: "cert-manager", Objects: decodeDocuments(t, out.Bytes())}}}
	rendered, err := RenderEgress(AddonInput{Cluster: testCluster(), Bundle: bundle, Settings: map[string]string{
		"egress.http-proxy":             "http://proxy.kube-system:10808",
		"egress.no-proxy":               "10.0.0.0/8,.svc",
		"letsencrypt.dns01-nameservers": "https://one.one.one.one/dns-query",
	}})
	if err != nil {
		t.Fatal(err)
	}
	args, env := controllerContainer(t, rendered)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dns01-recursive-nameservers=https://one.one.one.one/dns-query") || !strings.Contains(joined, "--dns01-recursive-nameservers-only=true") {
		t.Fatalf("args %v", args)
	}
	if env["HTTP_PROXY"] != "http://proxy.kube-system:10808" || env["HTTPS_PROXY"] != "http://proxy.kube-system:10808" || env["NO_PROXY"] != derivedNoProxy+",10.0.0.0/8" {
		t.Fatalf("env %v", env)
	}
}

func TestCertManagerChartHasNoEgressByDefault(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	cmd := exec.Command("helm", "template", "cert-manager", "charts/cert-manager", "--namespace", "cert-manager", "-f", "release/values/cert-manager.yaml")
	cmd.Dir = filepath.Join("..", "..")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template: %v: %s", err, stderr.String())
	}
	bundle := release.Bundle{Groups: []release.Group{{Name: "cert-manager", Objects: decodeDocuments(t, out.Bytes())}}}
	rendered, err := RenderEgress(AddonInput{Bundle: bundle, Settings: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	args, env := controllerContainer(t, rendered)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "dns01") {
		t.Fatalf("args %v", args)
	}
	if env["HTTP_PROXY"] != "" || env["HTTPS_PROXY"] != "" || env["NO_PROXY"] != "" {
		t.Fatalf("env %v", env)
	}
}
