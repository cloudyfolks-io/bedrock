package operator

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func rbacBundle(t *testing.T) release.Bundle {
	t.Helper()
	bundle := testBundle(t)
	bundle.Spec.Components = append(bundle.Spec.Components, v1alpha1.ReleaseComponent{Name: "rook", Version: "v1.20.7", Image: "quay.io/ceph/ceph:v20.2.4"})
	return bundle
}

func allows(role rbacv1.ClusterRole, group, resource, verb string) bool {
	return slices.ContainsFunc(role.Rules, func(rule rbacv1.PolicyRule) bool {
		return slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, verb)
	})
}

func TestOperatorRole(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if role.Name != "bedrock-operator" || role.Kind != "ClusterRole" || role.Labels[release.ComponentLabel] != "bedrock" {
		t.Fatalf("role meta %+v %+v", role.TypeMeta, role.ObjectMeta)
	}
	want := []struct{ group, resource, verb string }{
		{"", "configmaps", "patch"},
		{"", "namespaces", "create"},
		{"apiextensions.k8s.io", "customresourcedefinitions", "update"},
		{"snapshot.storage.k8s.io", "volumesnapshotclasses", "patch"},
		{"ceph.rook.io", "cephclusters", "create"},
		{"ceph.rook.io", "cephblockpools", "patch"},
		{"kubevirt.io", "kubevirts", "patch"},
		{"cdi.kubevirt.io", "cdis", "patch"},
		{"cert-manager.io", "certificates", "get"},
		{"cert-manager.io", "clusterissuers", "patch"},
		{"", "secrets", "get"},
		{"storage.k8s.io", "storageclasses", "patch"},
		{"apiextensions.k8s.io", "customresourcedefinitions", "patch"},
		{"", "namespaces", "patch"},
		{"bedrock.cloudyfolks.io", "clusters/status", "update"},
		{"bedrock.cloudyfolks.io", "nodeupgrades", "create"},
		{"bedrock.cloudyfolks.io", "settings/status", "patch"},
		{"", "nodes", "patch"},
		{"", "pods", "delete"},
		{"", "pods/eviction", "create"},
		{"", "pods/exec", "create"},
		{"coordination.k8s.io", "leases", "update"},
		{"apps", "controllerrevisions", "list"},
		{"kubevirt.io", "virtualmachines", "create"},
		{"kubevirt.io", "virtualmachineinstances", "get"},
		{"rbac.authorization.k8s.io", "clusterroles", "escalate"},
		{"rbac.authorization.k8s.io", "rolebindings", "bind"},
	}
	for _, rule := range want {
		if !allows(role, rule.group, rule.resource, rule.verb) {
			t.Errorf("role must allow %s %s/%s", rule.verb, rule.group, rule.resource)
		}
	}
	for _, rule := range role.Rules {
		if slices.Contains(rule.Verbs, "*") || slices.Contains(rule.Resources, "*") || slices.Contains(rule.APIGroups, "*") {
			t.Fatalf("no wildcard: %+v", rule)
		}
	}
	if allows(role, "", "pods", "create") || allows(role, "", "nodes", "delete") {
		t.Fatal("the operator never creates pods or deletes nodes")
	}
	if allows(role, "apiextensions.k8s.io", "customresourcedefinitions", "delete") || allows(role, "", "namespaces", "delete") {
		t.Fatal("the operator never deletes CRDs or namespaces")
	}
	if slices.ContainsFunc(role.Rules, func(rule rbacv1.PolicyRule) bool { return slices.Contains(rule.APIGroups, "autopilot.k0sproject.io") }) {
		t.Fatal("the operator no longer uses k0s autopilot")
	}
	again, err := OperatorRole(rbacBundle(t))
	if err != nil || !reflect.DeepEqual(again, role) {
		t.Fatalf("the role must be deterministic: %v", err)
	}
}

func TestOperatorRoleCoversEveryBedrockCRD(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "manifests", "00-crds", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("crds %v %v", files, err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := sigyaml.Unmarshal(body, &crd); err != nil {
			t.Fatal(err)
		}
		if !allows(role, crd.Spec.Group, crd.Spec.Names.Plural, "patch") {
			t.Errorf("role must cover %s/%s", crd.Spec.Group, crd.Spec.Names.Plural)
		}
		for _, version := range crd.Spec.Versions {
			if version.Subresources != nil && version.Subresources.Status != nil && !allows(role, crd.Spec.Group, crd.Spec.Names.Plural+"/status", "update") {
				t.Errorf("role must cover %s/%s/status", crd.Spec.Group, crd.Spec.Names.Plural)
			}
		}
	}
}

func TestOperatorRoleCanAlwaysPatchItself(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if !allows(role, "rbac.authorization.k8s.io", "clusterroles", "patch") {
		t.Fatal("the operator must patch its own ClusterRole on every reconcile, even when the release renders no other ClusterRole")
	}
	if !allows(role, "apps", "deployments", "get") || !allows(role, "apps", "deployments", "patch") {
		t.Fatal("the operator must read and patch its own Deployment to switch images, even when the release renders no Deployment")
	}
}

func TestRoleYAML(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := RoleYAML(role)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "creationTimestamp") || !strings.HasPrefix(string(body), "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\n") {
		t.Fatalf("yaml:\n%s", body)
	}
	var parsed rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &parsed); err != nil || !reflect.DeepEqual(parsed.Rules, role.Rules) {
		t.Fatalf("round trip: %v", err)
	}
}
