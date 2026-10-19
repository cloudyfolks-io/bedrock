package operator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func addonBundle(t *testing.T) release.Bundle {
	t.Helper()
	cfg, err := release.LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	selected := cfg.Components[:0:0]
	for _, component := range cfg.Components {
		if component.Name == "kata" || component.Name == "loki" || component.Name == "cnpg" || component.Name == "crds" {
			selected = append(selected, component)
		}
	}
	cfg.Components = selected
	out := t.TempDir()
	opts := release.BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("..", ".."), CacheDir: t.TempDir()}
	if err := release.Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := release.Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func ruleAllows(group, resource, verb string) bool {
	return slices.ContainsFunc(addonsRules(), func(rule rbacv1.PolicyRule) bool {
		return slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, verb)
	})
}

func TestAddonRulesCoverTheAddonManifests(t *testing.T) {
	bundle := addonBundle(t)
	for name, objects := range bundle.Addons {
		for _, obj := range objects {
			kind := obj.GroupVersionKind().GroupKind()
			resource := resourceOf(kind, nil)
			for _, verb := range verbsOfAddonObject(kind.Kind) {
				if !ruleAllows(kind.Group, resource, verb) {
					t.Errorf("addon %s: no %s on %s/%s", name, verb, kind.Group, resource)
				}
			}
		}
	}
}

func TestAddonRulesCoverTheAddonKind(t *testing.T) {
	for _, resource := range []string{"addons", "addons/status", "addons/finalizers"} {
		for _, verb := range []string{"get", "list", "watch", "update", "patch"} {
			if !ruleAllows("bedrock.cloudyfolks.io", resource, verb) {
				t.Errorf("no %s on %s", verb, resource)
			}
		}
	}
}

func TestAddonRulesLetTheReconcilerCreateTheMonitoringNamespace(t *testing.T) {
	for _, verb := range []string{"get", "create"} {
		if !ruleAllows("", "namespaces", verb) {
			t.Errorf("no %s on namespaces", verb)
		}
	}
}

func TestOperatorRoleCarriesTheAddonRules(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ group, resource, verb string }{
		{"node.k8s.io", "runtimeclasses", "create"},
		{"", "secrets", "create"},
		{"", "secrets", "patch"},
		{"apps", "statefulsets", "delete"},
		{"bedrock.cloudyfolks.io", "addons/finalizers", "update"},
	} {
		if !allows(role, check.group, check.resource, check.verb) {
			t.Errorf("operator role lacks %s on %s", check.verb, check.resource)
		}
	}
}

func TestAgentCanReadOnlyTheAuditPolicy(t *testing.T) {
	admin, cfg := StartTestEnv(t)
	ctx := context.Background()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}}); err != nil {
		t.Fatal(err)
	}
	applyManifest(t, ctx, admin, filepath.Join("..", "..", "manifests", "90-bedrock", "agent-audit-rbac.yaml"))
	for _, obj := range []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-audit-policy", Namespace: "bedrock-system"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "bedrock-system"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "any", Namespace: "bedrock-system"}},
	} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	node := impersonateAs(t, cfg, "system:node:x", []string{"system:nodes", "system:authenticated"})
	var policy corev1.ConfigMap
	if err := node.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "bedrock-audit-policy"}, &policy); err != nil {
		t.Fatalf("a node must read the audit policy: %v", err)
	}
	var other corev1.ConfigMap
	if err := node.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "other"}, &other); !apierrors.IsForbidden(err) {
		t.Fatalf("a node must not read another ConfigMap, got %v", err)
	}
	var secret corev1.Secret
	if err := node.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "any"}, &secret); !apierrors.IsForbidden(err) {
		t.Fatalf("a node must not read a Secret, got %v", err)
	}
}

func verbsOfAddonObject(kind string) []string {
	if kind == "Namespace" {
		return []string{"create", "patch", "get"}
	}
	return []string{"create", "patch", "delete", "get", "list", "watch"}
}
