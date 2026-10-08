package operator

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func proxyInput(host, mode string) AddonInput {
	in := authnInput(host, mode)
	in.ProxyInstalled = true
	return in
}

func proxyMatches(t *testing.T, obj *unstructured.Unstructured) []string {
	t.Helper()
	routes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "routes")
	matches := make([]string, 0, len(routes))
	for _, item := range routes {
		route := item.(map[string]any)
		match, _, _ := unstructured.NestedString(route, "match")
		services, _, _ := unstructured.NestedSlice(route, "services")
		service := services[0].(map[string]any)
		if service["name"] != "bedrock-proxy" || service["port"] != int64(443) || service["scheme"] != "https" || service["serversTransport"] != "bedrock-proxy" {
			t.Fatalf("route service %+v", service)
		}
		matches = append(matches, match)
	}
	return matches
}

func TestRenderProxyObjects(t *testing.T) {
	out, err := RenderProxy(proxyInput("lab.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "" || len(out.Objects) != 3 {
		t.Fatalf("rendered %+v", out)
	}
	for _, obj := range out.Objects {
		if obj.GetNamespace() != release.SystemNamespace || obj.GetLabels()[v1alpha1.LabelKind] != obj.GetKind() || obj.GetLabels()[v1alpha1.LabelName] != obj.GetName() {
			t.Fatalf("object %s %s meta %s %v", obj.GetKind(), obj.GetName(), obj.GetNamespace(), obj.GetLabels())
		}
	}
	route := findObject(out.Objects, "IngressRoute", "bedrock-proxy")
	if route == nil || route.GetAPIVersion() != "traefik.io/v1alpha1" {
		t.Fatalf("objects %+v", out.Objects)
	}
	entryPoints, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	tls, found, _ := unstructured.NestedMap(route.Object, "spec", "tls")
	if len(entryPoints) != 1 || entryPoints[0] != "websecure" || !found || len(tls) != 0 {
		t.Fatalf("route spec %+v", route.Object["spec"])
	}
	want := []string{
		"Host(`console.lab.example`) && !Path(`/healthz`) && !Path(`/readyz`)",
		"Host(`api.lab.example`) && !Path(`/healthz`) && !Path(`/readyz`)",
		"Host(`lab.example`) && !Path(`/healthz`) && !Path(`/readyz`)",
	}
	got := proxyMatches(t, route)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("matches %v", got)
	}
	if len(out.Probes) != 1 || out.Probes[0].GVK.Kind != "Deployment" || out.Probes[0].Key.Name != "bedrock-proxy" || out.Probes[0].Key.Namespace != release.SystemNamespace {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestProxyAddonFollowsPlatformHost(t *testing.T) {
	first, err := RenderProxy(proxyInput("a.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderProxy(proxyInput("b.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range proxyMatches(t, findObject(second.Objects, "IngressRoute", "bedrock-proxy")) {
		if strings.Contains(match, "a.example") || !strings.Contains(match, "b.example") {
			t.Fatalf("match %q", match)
		}
	}
	if strings.Join(proxyMatches(t, findObject(first.Objects, "IngressRoute", "bedrock-proxy")), "") == strings.Join(proxyMatches(t, findObject(second.Objects, "IngressRoute", "bedrock-proxy")), "") {
		t.Fatal("the routes did not change with the host")
	}
}

func TestProxyAddonDegradesWithoutHost(t *testing.T) {
	out, err := RenderProxy(proxyInput("", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "PlatformHostNotSet" || len(out.Objects) != 0 {
		t.Fatalf("rendered %+v", out)
	}
}

func TestProxyAddonWaitsForPlatformCA(t *testing.T) {
	in := proxyInput("lab.example", "SelfSigned")
	in.PlatformCA = nil
	out, err := RenderProxy(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "WaitingForPlatformCA" || len(out.Objects) != 0 {
		t.Fatalf("rendered %+v", out)
	}
}

func TestProxyAddonSkipsWhenNotInstalled(t *testing.T) {
	in := proxyInput("lab.example", "SelfSigned")
	in.ProxyInstalled = false
	out, err := RenderProxy(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "ProxyNotInstalled" || len(out.Objects) != 0 {
		t.Fatalf("rendered %+v", out)
	}
}

func TestProxyNetworkPolicyUsesTheJoinCIDR(t *testing.T) {
	for cidr, join := range map[string]string{"100.64.0.0/16": "", "100.99.0.0/16": "100.99.0.0/16"} {
		in := proxyInput("lab.example", "SelfSigned")
		in.Cluster.Spec.JoinCIDR = join
		out, err := RenderProxy(in)
		if err != nil {
			t.Fatal(err)
		}
		policy := findObject(out.Objects, "NetworkPolicy", "bedrock-proxy")
		podSelector, _, _ := unstructured.NestedStringMap(policy.Object, "spec", "podSelector", "matchLabels")
		ingress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "ingress")
		if podSelector["app"] != "bedrock-proxy" || len(ingress) != 2 {
			t.Fatalf("policy %+v", policy.Object["spec"])
		}
		traefik := ingress[0].(map[string]any)["from"].([]any)[0].(map[string]any)
		namespaceSelector, _, _ := unstructured.NestedStringMap(traefik, "namespaceSelector", "matchLabels")
		block, _, _ := unstructured.NestedString(ingress[1].(map[string]any)["from"].([]any)[0].(map[string]any), "ipBlock", "cidr")
		if namespaceSelector["kubernetes.io/metadata.name"] != "traefik" || block != cidr {
			t.Fatalf("traefik %+v, cidr %q want %q", namespaceSelector, block, cidr)
		}
		for _, rule := range ingress {
			ports, _, _ := unstructured.NestedSlice(rule.(map[string]any), "ports")
			port := ports[0].(map[string]any)
			if port["protocol"] != "TCP" || port["port"] != "https" {
				t.Fatalf("port %+v", port)
			}
		}
	}
}

func TestProxyPlatformCAConfigMap(t *testing.T) {
	selfSigned, err := RenderProxy(proxyInput("lab.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	custom := proxyInput("lab.example", "Custom")
	custom.CustomCA = []byte("CUSTOM")
	customOut, err := RenderProxy(custom)
	if err != nil {
		t.Fatal(err)
	}
	letsEncrypt, err := RenderProxy(proxyInput("lab.example", "LetsEncrypt"))
	if err != nil {
		t.Fatal(err)
	}
	for mode, tc := range map[string]struct {
		out  Rendered
		want string
	}{"SelfSigned": {selfSigned, testPlatformCA}, "Custom": {customOut, "CUSTOM"}, "LetsEncrypt": {letsEncrypt, ""}} {
		configMap := findObject(tc.out.Objects, "ConfigMap", "bedrock-platform-ca")
		data, _, _ := unstructured.NestedStringMap(configMap.Object, "data")
		if data["ca.crt"] != tc.want || len(data) != 1 || strings.Contains(data["ca.crt"], "PRIVATE KEY") {
			t.Errorf("%s: data %v", mode, data)
		}
	}
}

func TestAuthnNetworkPolicyAllowsTheProxy(t *testing.T) {
	out, err := RenderAuthn(authnInput("lab.example", "SelfSigned"))
	if err != nil {
		t.Fatal(err)
	}
	policy := findObject(out.Objects, "NetworkPolicy", "bedrock-authn")
	ingress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	found := false
	for _, rule := range ingress {
		from, _, _ := unstructured.NestedSlice(rule.(map[string]any), "from")
		for _, item := range from {
			peer := item.(map[string]any)
			pods, _, _ := unstructured.NestedStringMap(peer, "podSelector", "matchLabels")
			namespaces, _, _ := unstructured.NestedStringMap(peer, "namespaceSelector", "matchLabels")
			found = found || pods["app"] == "bedrock-proxy" && namespaces["kubernetes.io/metadata.name"] == "bedrock-system"
		}
	}
	if !found {
		t.Fatalf("ingress %+v", ingress)
	}
}

func TestAddonInputReadsTheProxyDeployment(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	cluster := v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}}
	r := &AddonReconciler{Client: c}
	in, err := r.input(ctx, cluster)
	if err != nil || in.ProxyInstalled {
		t.Fatalf("before: %+v, %v", in.ProxyInstalled, err)
	}
	labels := map[string]string{"app": "bedrock-proxy"}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: "bedrock-proxy"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: "ghcr.io/cloudyfolks-io/bedrock:dev"}}}},
		},
	}
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	in, err = r.input(ctx, cluster)
	if err != nil || !in.ProxyInstalled {
		t.Fatalf("after: %+v, %v", in.ProxyInstalled, err)
	}
}

func TestOperatorRoleIsUnchangedByTheProxyAddon(t *testing.T) {
	for _, in := range sampleAddonInputs(rbacBundle(t)) {
		out, err := RenderProxy(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Objects) != 0 || len(out.Probes) != 0 {
			t.Fatalf("the sample inputs must add no kinds through the proxy addon: %+v", out)
		}
	}
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []struct{ group, resource, verb string }{
		{"traefik.io", "ingressroutes", "patch"},
		{"networking.k8s.io", "networkpolicies", "patch"},
		{"", "configmaps", "patch"},
		{"", "secrets", "create"},
		{"apps", "deployments", "get"},
	} {
		if !allows(role, rule.group, rule.resource, rule.verb) {
			t.Errorf("the operator role must allow %s %s/%s for the proxy addon", rule.verb, rule.group, rule.resource)
		}
	}
}
