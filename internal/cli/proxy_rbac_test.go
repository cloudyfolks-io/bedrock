package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/compute"
	"github.com/cloudyfolks-io/bedrock/internal/images"
)

func proxyBaseRole(t *testing.T) rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "88-proxy", "10-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var role rbacv1.ClusterRole
		if err := decoder.Decode(&role); err == io.EOF {
			t.Fatal("no bedrock-proxy-base ClusterRole")
		} else if err != nil {
			t.Fatal(err)
		}
		if role.Kind == "ClusterRole" && role.Name == "bedrock-proxy-base" {
			return role
		}
	}
}

func ruleKeys(rules []rbacv1.PolicyRule) []string {
	var keys []string
	for _, rule := range rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					keys = append(keys, group+"|"+resource+"|"+verb+"|"+strings.Join(rule.ResourceNames, ","))
				}
			}
		}
	}
	return keys
}

func TestProxyBaseRoleGrantsExactlyTheNeededRules(t *testing.T) {
	got := ruleKeys(proxyBaseRole(t).Rules)
	want := []string{
		"bedrock.cloudyfolks.io|settings|get|platform.host",
		"bedrock.cloudyfolks.io|projects|list|",
	}
	want = append(want, ruleKeys(compute.ProxyRules())...)
	want = append(want, ruleKeys(images.UploadHandlerRules())...)
	for _, key := range got {
		if !slices.Contains(want, key) {
			t.Errorf("the base role grants more than needed: %s", key)
		}
	}
	for _, key := range want {
		if !slices.Contains(got, key) {
			t.Errorf("the base role lacks %s", key)
		}
	}
}
