package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const corednsConfigManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
  namespace: kube-system
data:
  Corefile: |
    .:53 {
        forward . ${BEDROCK_DNS_FORWARD}
    }
`

func initWithCoreDNSRelease(t *testing.T, extraSpec string) string {
	t.Helper()
	run := prepareInit(t)
	releaseDir := filepath.Join(t.TempDir(), "release")
	if err := os.CopyFS(releaseDir, os.DirFS(filepath.Join("..", "release", "testdata", "good"))); err != nil {
		t.Fatal(err)
	}
	groupDir := filepath.Join(releaseDir, "manifests", "22-coredns")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groupDir, "config.yaml"), []byte(corednsConfigManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	config := strings.Replace(initConfig, "  roles:", extraSpec+"  roles:", 1)
	if err := os.WriteFile(run.args[1], []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	run.args[3] = releaseDir
	runPreparedInit(t, run)
	var configMap corev1.ConfigMap
	if err := run.c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "coredns"}, &configMap); err != nil {
		t.Fatal(err)
	}
	return configMap.Data["Corefile"]
}

func TestInitForwardsClusterDNSToTheConfiguredUpstreams(t *testing.T) {
	corefile := initWithCoreDNSRelease(t, "  dns:\n    upstreams: [217.218.127.127, 217.218.155.155]\n")
	if !strings.Contains(corefile, "forward . 217.218.127.127 217.218.155.155\n") || strings.Contains(corefile, "resolv.conf") {
		t.Fatalf("corefile %q", corefile)
	}
}

func TestInitForwardsClusterDNSToTheNodeResolvConfWithoutUpstreams(t *testing.T) {
	corefile := initWithCoreDNSRelease(t, "")
	if !strings.Contains(corefile, "forward . /etc/resolv.conf\n") {
		t.Fatalf("corefile %q", corefile)
	}
}
