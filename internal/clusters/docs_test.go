package clusters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clustersDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "clusters.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDocsDescribeEveryReason(t *testing.T) {
	text := clustersDoc(t)
	for _, reason := range Reasons() {
		if !strings.Contains(text, "| `"+reason+"` |") {
			t.Errorf("docs/clusters.md has no table row for the reason %s", reason)
		}
	}
}

func TestDocsNameTheContractValues(t *testing.T) {
	text := clustersDoc(t)
	for _, name := range []string{
		VersionsSetting, "clusters.enabled", "count/kubernetesclusters.bedrock.cloudyfolks.io", "6443", "8132", "9443",
		"bastion", "ElasticIP", "kubectl delete node", "https://get.k0s.sh", "100.64.0.0/16", "100.65.0.0/16",
		KubeconfigSecretName("<cluster>"), "status.kubeconfigSecret", "bedrock.cloudyfolks.io/kubernetescluster", "offset 10",
	} {
		if !strings.Contains(text, name) {
			t.Errorf("docs/clusters.md does not mention %s", name)
		}
	}
}
