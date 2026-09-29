package operator

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func OperatorRole(bundle release.Bundle) (rbacv1.ClusterRole, error) {
	objects := releaseObjects(bundle)
	addonKinds, err := renderedAddonKinds(bundle)
	if err != nil {
		return rbacv1.ClusterRole{}, err
	}
	plurals := crdPlurals(objects)
	var rules []rbacv1.PolicyRule
	for _, kind := range groupKinds(append(objectKinds(objects), addonKinds...)) {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{kind.Group}, Resources: []string{resourceOf(kind, plurals)}, Verbs: verbsFor(kind)})
	}
	return rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment, Labels: map[string]string{release.ComponentLabel: "bedrock"}},
		Rules:      mergeRules(append(rules, fixedRules()...)),
	}, nil
}

func ensureOperatorRole(ctx context.Context, c client.Client, bundle release.Bundle) error {
	role, err := OperatorRole(bundle)
	if err != nil {
		return err
	}
	return ssa.Apply(ctx, c, &role, v1alpha1.OperatorFieldManager)
}

func RoleYAML(role rbacv1.ClusterRole) ([]byte, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&role)
	if err != nil {
		return nil, err
	}
	unstructured.RemoveNestedField(content, "metadata", "creationTimestamp")
	return sigyaml.Marshal(content)
}

func standardVerbs() []string {
	return []string{"create", "delete", "get", "list", "patch", "update", "watch"}
}

func verbsFor(kind schema.GroupKind) []string {
	if neverDeleted(kind) {
		return standardVerbsWithoutDelete()
	}
	return standardVerbs()
}

func neverDeleted(kind schema.GroupKind) bool {
	return kind == (schema.GroupKind{Kind: "Namespace"}) || kind == (schema.GroupKind{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"})
}

func standardVerbsWithoutDelete() []string {
	return []string{"create", "get", "list", "patch", "update", "watch"}
}

func fixedRules() []rbacv1.PolicyRule {
	read := []string{"get", "list", "watch"}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: bedrockResources(), Verbs: standardVerbs()},
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "patch", "update", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"delete", "get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods/eviction"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create", "get"}},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
		{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: standardVerbs()},
		{APIGroups: []string{"apps"}, Resources: []string{"controllerrevisions"}, Verbs: read},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "patch"}},
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: read},
		{APIGroups: []string{vmGVK.Group}, Resources: []string{"virtualmachineinstances", "virtualmachines"}, Verbs: standardVerbs()},
		{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterrolebindings", "clusterroles", "rolebindings", "roles"}, Verbs: []string{"bind", "escalate"}},
		{APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterroles"}, Verbs: []string{"patch"}},
	}
}

func bedrockResources() []string {
	return []string{
		"apitokens", "apitokens/status",
		"authcodes", "authcodes/status",
		"authrequests", "authrequests/status",
		"clusterconfigs",
		"clusters", "clusters/status",
		"credentials", "credentials/status",
		"devicerequests", "devicerequests/status",
		"groups", "groups/status",
		"hostconfigs", "hostconfigs/status",
		"hosts", "hosts/status",
		"identityproviders", "identityproviders/status",
		"nodeupgrades", "nodeupgrades/status",
		"oauthclients", "oauthclients/status",
		"refreshtokens", "refreshtokens/status",
		"releases", "releases/status",
		"sessions", "sessions/status",
		"settings", "settings/status",
		"signingkeys", "signingkeys/status",
		"users", "users/status",
	}
}

func releaseObjects(bundle release.Bundle) []*unstructured.Unstructured {
	var objects []*unstructured.Unstructured
	for _, group := range bundle.Groups {
		objects = append(objects, group.Objects...)
	}
	return objects
}

func objectKinds(objects []*unstructured.Unstructured) []schema.GroupVersionKind {
	kinds := make([]schema.GroupVersionKind, 0, len(objects))
	for _, obj := range objects {
		kinds = append(kinds, obj.GroupVersionKind())
	}
	return kinds
}

func renderedAddonKinds(bundle release.Bundle) ([]schema.GroupVersionKind, error) {
	var kinds []schema.GroupVersionKind
	for _, input := range sampleAddonInputs(bundle) {
		for _, addon := range DefaultAddons() {
			rendered, err := addon.Render(input)
			if err != nil {
				return nil, fmt.Errorf("%s addon: %w", addon.Name, err)
			}
			kinds = append(kinds, objectKinds(rendered.Objects)...)
			for _, probe := range rendered.Probes {
				kinds = append(kinds, probe.GVK)
			}
		}
	}
	return kinds, nil
}

func sampleAddonInputs(bundle release.Bundle) []AddonInput {
	cluster := v1alpha1.Cluster{Spec: v1alpha1.ClusterSpec{API: v1alpha1.APISpec{VIP: "10.0.0.10"}}}
	hosts := []v1alpha1.Host{{ObjectMeta: metav1.ObjectMeta{Name: "sample"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleCephOSD}, Storage: v1alpha1.HostStorageSpec{Devices: []string{"/dev/sample"}}}}}
	secret := &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("crt"), "tls.key": []byte("key")}}
	ca := &corev1.Secret{Data: map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("ca"), "tls.key": []byte("key")}}
	letsEncrypt := withSettings(catalogDefaults(), map[string]string{"platform.tls-mode": "LetsEncrypt", "letsencrypt.email": "ops@example.com"})
	custom := withSettings(catalogDefaults(), map[string]string{"platform.tls-mode": "Custom", "platform.custom-tls": "sample"})
	return []AddonInput{
		{Cluster: cluster, Hosts: hosts, Settings: catalogDefaults(), Bundle: bundle, PlatformCA: ca},
		{Cluster: cluster, Hosts: hosts, Settings: letsEncrypt, Bundle: bundle, PlatformCA: ca},
		{Cluster: cluster, Hosts: hosts, Settings: custom, Bundle: bundle, CustomTLS: secret, PlatformCA: ca},
	}
}

func catalogDefaults() map[string]string {
	values := map[string]string{}
	for _, def := range settings.Catalog() {
		values[def.Key] = def.Default
	}
	return values
}

func withSettings(base, overrides map[string]string) map[string]string {
	merged := maps.Clone(base)
	maps.Copy(merged, overrides)
	return merged
}

func crdPlurals(objects []*unstructured.Unstructured) map[schema.GroupKind]string {
	plurals := map[schema.GroupKind]string{}
	for _, obj := range objects {
		if obj.GetKind() != "CustomResourceDefinition" {
			continue
		}
		group, _, _ := unstructured.NestedString(obj.Object, "spec", "group")
		kind, _, _ := unstructured.NestedString(obj.Object, "spec", "names", "kind")
		plural, _, _ := unstructured.NestedString(obj.Object, "spec", "names", "plural")
		plurals[schema.GroupKind{Group: group, Kind: kind}] = plural
	}
	return plurals
}

func groupKinds(kinds []schema.GroupVersionKind) []schema.GroupKind {
	var unique []schema.GroupKind
	for _, kind := range kinds {
		if groupKind := kind.GroupKind(); groupKind.Kind != "" && !slices.Contains(unique, groupKind) {
			unique = append(unique, groupKind)
		}
	}
	return unique
}

func resourceOf(kind schema.GroupKind, plurals map[schema.GroupKind]string) string {
	if plural, ok := plurals[kind]; ok {
		return plural
	}
	resource, _ := meta.UnsafeGuessKindToResource(kind.WithVersion(""))
	return resource.Resource
}

func mergeRules(rules []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	resources := map[string][]string{}
	keys := map[string]rbacv1.PolicyRule{}
	for _, rule := range rules {
		verbs := slices.Sorted(slices.Values(rule.Verbs))
		for _, group := range rule.APIGroups {
			key := group + "|" + strings.Join(verbs, ",")
			keys[key] = rbacv1.PolicyRule{APIGroups: []string{group}, Verbs: verbs}
			for _, resource := range rule.Resources {
				if !slices.Contains(resources[key], resource) {
					resources[key] = append(resources[key], resource)
				}
			}
		}
	}
	merged := make([]rbacv1.PolicyRule, 0, len(keys))
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if len(resources[key]) == 0 {
			continue
		}
		rule := keys[key]
		rule.Resources = slices.Sorted(slices.Values(resources[key]))
		merged = append(merged, rule)
	}
	return merged
}
