package operator

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func uploadInput(host, ca string) AddonInput {
	return AddonInput{Settings: map[string]string{"platform.host": host}, UploadProxyCA: []byte(ca)}
}

func objectFromYAML(t *testing.T, text string) *unstructured.Unstructured {
	t.Helper()
	body, err := sigyaml.YAMLToJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(body); err != nil {
		t.Fatal(err)
	}
	return obj
}

const uploadWantYAML = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: bedrock-upload-ca
  namespace: cdi
  labels: {bedrock.cloudyfolks.io/kind: ConfigMap, bedrock.cloudyfolks.io/name: bedrock-upload-ca}
data:
  ca.crt: ca-bundle-pem
---
apiVersion: traefik.io/v1alpha1
kind: ServersTransport
metadata:
  name: bedrock-upload
  namespace: cdi
  labels: {bedrock.cloudyfolks.io/kind: ServersTransport, bedrock.cloudyfolks.io/name: bedrock-upload}
spec:
  serverName: cdi-uploadproxy.cdi.svc
  rootCAs:
    - configMap: bedrock-upload-ca
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: bedrock-upload-cors
  namespace: cdi
  labels: {bedrock.cloudyfolks.io/kind: Middleware, bedrock.cloudyfolks.io/name: bedrock-upload-cors}
spec:
  headers:
    accessControlAllowMethods: [POST, OPTIONS]
    accessControlAllowHeaders: [Authorization, Content-Type]
    accessControlAllowOriginList: ["https://console.cloud.example.com"]
    accessControlMaxAge: 600
    addVaryHeader: true
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: bedrock-upload
  namespace: cdi
  labels: {bedrock.cloudyfolks.io/kind: IngressRoute, bedrock.cloudyfolks.io/name: bedrock-upload}
spec:
  entryPoints: [websecure]
  routes:
    - match: Host(` + "`upload.cloud.example.com`" + `) && PathPrefix(` + "`/v1beta1/upload`" + `)
      kind: Rule
      middlewares:
        - name: bedrock-upload-cors
      services:
        - name: cdi-uploadproxy
          port: 443
          scheme: https
          serversTransport: bedrock-upload
  tls: {}
`

func TestRenderUploadObjects(t *testing.T) {
	out, err := RenderUpload(uploadInput("cloud.example.com", "ca-bundle-pem"))
	if err != nil {
		t.Fatal(err)
	}
	documents := strings.Split(strings.TrimSpace(uploadWantYAML), "\n---\n")
	if out.SkipReason != "" || len(out.Objects) != len(documents) {
		t.Fatalf("rendered %+v", out)
	}
	for i, document := range documents {
		want := objectFromYAML(t, document)
		if !reflect.DeepEqual(out.Objects[i].Object, want.Object) {
			t.Errorf("object %d\n got %v\nwant %v", i, out.Objects[i].Object, want.Object)
		}
	}
	if len(out.Probes) != 1 || out.Probes[0].Key.Namespace != "cdi" || out.Probes[0].Key.Name != "cdi-uploadproxy" || out.Probes[0].GVK.Kind != "Deployment" {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestRenderUploadWithoutHost(t *testing.T) {
	out, err := RenderUpload(uploadInput("", "pem"))
	if err != nil || out.SkipReason != "PlatformHostNotSet" || len(out.Objects) != 0 {
		t.Fatalf("rendered %+v %v", out, err)
	}
}

func TestRenderUploadWithoutCA(t *testing.T) {
	out, err := RenderUpload(uploadInput("cloud.example.com", ""))
	if err != nil || out.SkipReason != "WaitingForUploadProxyCA" || len(out.Objects) != 0 {
		t.Fatalf("rendered %+v %v", out, err)
	}
}

func TestUploadAddonFollowsPlatformHost(t *testing.T) {
	first, err := RenderUpload(uploadInput("a.example.com", "pem"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderUpload(uploadInput("b.example.com", "pem"))
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first.Objects, second.Objects) {
		t.Fatal("the objects must follow the host")
	}
	for _, obj := range second.Objects {
		body, err := obj.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "a.example.com") {
			t.Fatalf("%s still names the old host", obj.GetKind())
		}
	}
	route := findObject(second.Objects, "IngressRoute", "bedrock-upload")
	cors := findObject(second.Objects, "Middleware", "bedrock-upload-cors")
	routes, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	origins, _, _ := unstructured.NestedStringSlice(cors.Object, "spec", "headers", "accessControlAllowOriginList")
	if routes[0].(map[string]any)["match"] != "Host(`upload.b.example.com`) && PathPrefix(`/v1beta1/upload`)" || len(origins) != 1 || origins[0] != "https://console.b.example.com" {
		t.Fatalf("route %v origins %v", routes, origins)
	}
}

func TestUploadAddonFollowsTheCDICA(t *testing.T) {
	first, _ := RenderUpload(uploadInput("cloud.example.com", "pem-1"))
	second, _ := RenderUpload(uploadInput("cloud.example.com", "pem-2"))
	for i := range first.Objects {
		same := reflect.DeepEqual(first.Objects[i].Object, second.Objects[i].Object)
		if (first.Objects[i].GetKind() == "ConfigMap") == same {
			t.Fatalf("%s: unexpected equality %v", first.Objects[i].GetKind(), same)
		}
	}
}

func TestInputReadsTheUploadProxyCA(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	cluster := v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}}
	r := &AddonReconciler{Client: c}

	in, err := r.input(ctx, cluster)
	if err != nil || len(in.UploadProxyCA) != 0 {
		t.Fatalf("without the bundle: %q %v", in.UploadProxyCA, err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cdi"}}); err != nil {
		t.Fatal(err)
	}
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cdi", Name: "cdi-uploadproxy-signer-bundle"}, Data: map[string]string{"ca-bundle.crt": "pem"}}
	if err := c.Create(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	in, err = r.input(ctx, cluster)
	if err != nil || string(in.UploadProxyCA) != "pem" {
		t.Fatalf("with the bundle: %q %v", in.UploadProxyCA, err)
	}
}

func TestDefaultAddonsIncludeUpload(t *testing.T) {
	for _, addon := range DefaultAddons() {
		if addon.Name == "upload" {
			if addon.Condition != "UploadReady" {
				t.Fatalf("condition %s", addon.Condition)
			}
			return
		}
	}
	t.Fatal("DefaultAddons must include upload")
}

func TestOperatorRoleCoversTheUploadRoute(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"ingressroutes", "serverstransports", "middlewares"} {
		if !allows(role, "traefik.io", resource, "patch") {
			t.Errorf("role must allow patch on traefik.io %s", resource)
		}
	}
}
