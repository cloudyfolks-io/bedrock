package objectstorage

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEndpointObjectsAreEmptyWithoutAHost(t *testing.T) {
	if objects := EndpointObjects(Rules{}, "SelfSigned"); len(objects) != 0 {
		t.Fatalf("objects %+v", objects)
	}
	if names := HostingDNSNames(Rules{PublicAliases: []string{"s3.alias.test"}}); names != nil {
		t.Fatalf("names %v", names)
	}
}

func TestEndpointObjectsForOneHost(t *testing.T) {
	objects := EndpointObjects(Rules{PublicHost: "s3.example.test"}, "SelfSigned")
	if len(objects) != 2 {
		t.Fatalf("objects %+v", objects)
	}
	wantCertificate := map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]any{
			"name":      "objectstorage-public",
			"namespace": "rook-ceph",
			"labels":    map[string]any{"bedrock.cloudyfolks.io/kind": "Certificate", "bedrock.cloudyfolks.io/name": "objectstorage-public"},
		},
		"spec": map[string]any{
			"secretName": "objectstorage-public-tls",
			"dnsNames":   []any{"s3.example.test"},
			"issuerRef":  map[string]any{"kind": "ClusterIssuer", "name": "bedrock-ca"},
		},
	}
	wantRoute := map[string]any{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "IngressRoute",
		"metadata": map[string]any{
			"name":      "objectstorage-public",
			"namespace": "rook-ceph",
			"labels":    map[string]any{"bedrock.cloudyfolks.io/kind": "IngressRoute", "bedrock.cloudyfolks.io/name": "objectstorage-public"},
		},
		"spec": map[string]any{
			"entryPoints": []any{"websecure"},
			"routes": []any{map[string]any{
				"match":    "(Host(`s3.example.test`)) && !PathPrefix(`/admin`)",
				"kind":     "Rule",
				"services": []any{map[string]any{"name": "rook-ceph-rgw-bedrock-rgw", "port": int64(80)}},
			}},
			"tls": map[string]any{"secretName": "objectstorage-public-tls"},
		},
	}
	if !reflect.DeepEqual(objects[0].Object, wantCertificate) {
		t.Fatalf("certificate %+v", objects[0].Object)
	}
	if !reflect.DeepEqual(objects[1].Object, wantRoute) {
		t.Fatalf("route %+v", objects[1].Object)
	}
}

func TestEndpointObjectsWithAliasesAddVirtualHostRules(t *testing.T) {
	rules := Rules{PublicHost: "s3.example.test", PublicAliases: []string{"s3.alias.test"}}
	objects := EndpointObjects(rules, "SelfSigned")
	names, _, _ := unstructured.NestedStringSlice(objects[0].Object, "spec", "dnsNames")
	wantNames := []string{"s3.example.test", "s3.alias.test", "*.s3.example.test", "*.s3.alias.test"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("dnsNames %v", names)
	}
	routes, _, _ := unstructured.NestedSlice(objects[1].Object, "spec", "routes")
	match := routes[0].(map[string]any)["match"]
	want := "(Host(`s3.example.test`) || Host(`s3.alias.test`) || HostRegexp(`^[a-z0-9-]+\\.s3\\.example\\.test$`) || HostRegexp(`^[a-z0-9-]+\\.s3\\.alias\\.test$`)) && !PathPrefix(`/admin`)"
	if match != want {
		t.Fatalf("match %q", match)
	}
}

func TestEndpointCertificateUsesLetsEncryptWithoutWildcards(t *testing.T) {
	rules := Rules{PublicHost: "s3.example.test", PublicAliases: []string{"s3.alias.test"}}
	objects := EndpointObjects(rules, "LetsEncrypt")
	issuer, _, _ := unstructured.NestedString(objects[0].Object, "spec", "issuerRef", "name")
	names, _, _ := unstructured.NestedStringSlice(objects[0].Object, "spec", "dnsNames")
	if issuer != "letsencrypt" || !reflect.DeepEqual(names, []string{"s3.example.test", "s3.alias.test"}) {
		t.Fatalf("issuer %q names %v", issuer, names)
	}
	for _, mode := range []string{"", "Custom", "SelfSigned"} {
		got, _, _ := unstructured.NestedString(EndpointObjects(rules, mode)[0].Object, "spec", "issuerRef", "name")
		if got != "bedrock-ca" {
			t.Fatalf("mode %q gave issuer %q", mode, got)
		}
	}
}

func TestHostingDNSNamesHoldTheHostTheAliasesAndTheServiceName(t *testing.T) {
	got := HostingDNSNames(Rules{PublicHost: "s3.example.test", PublicAliases: []string{"s3.alias.test"}})
	want := []string{"s3.example.test", "s3.alias.test", "rook-ceph-rgw-bedrock-rgw.rook-ceph.svc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v", got)
	}
}

func TestPublicEndpointFallsBackToTheServiceURL(t *testing.T) {
	if got := PublicEndpoint(Rules{}, "http://svc:80"); got != "http://svc:80" {
		t.Fatalf("got %q", got)
	}
	if got := PublicEndpoint(Rules{PublicHost: "s3.example.test"}, "http://svc:80"); got != "https://s3.example.test" {
		t.Fatalf("got %q", got)
	}
}

func endpointWorld(t *testing.T, values map[string]string) (client.Client, *EndpointReconciler) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for key, value := range values {
		builder = builder.WithObjects(&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}, Spec: v1alpha1.SettingSpec{Value: value}})
	}
	c := builder.Build()
	return c, &EndpointReconciler{Client: c}
}

func endpointExists(t *testing.T, c client.Client, apiVersion, kind string) bool {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	err := c.Get(context.Background(), client.ObjectKey{Namespace: StoreNamespace, Name: "objectstorage-public"}, obj)
	if err != nil && !meta.IsNoMatchError(err) && client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
	return err == nil
}

func TestEndpointReconcilerMakesAndRemovesTheObjects(t *testing.T) {
	c, r := endpointWorld(t, map[string]string{"objectstorage.public-host": "s3.example.test"})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if !endpointExists(t, c, "cert-manager.io/v1", "Certificate") || !endpointExists(t, c, "traefik.io/v1alpha1", "IngressRoute") {
		t.Fatal("the objects were not made")
	}
	var setting v1alpha1.Setting
	if err := c.Get(context.Background(), client.ObjectKey{Name: "objectstorage.public-host"}, &setting); err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = ""
	if err := c.Update(context.Background(), &setting); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if endpointExists(t, c, "cert-manager.io/v1", "Certificate") || endpointExists(t, c, "traefik.io/v1alpha1", "IngressRoute") {
		t.Fatal("the objects stayed")
	}
}

func TestEndpointReconcilerDoesNothingWithoutAHost(t *testing.T) {
	c, r := endpointWorld(t, map[string]string{})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if endpointExists(t, c, "cert-manager.io/v1", "Certificate") {
		t.Fatal("an object was made")
	}
}

func TestEndpointReconcilerFollowsTheTLSMode(t *testing.T) {
	c, r := endpointWorld(t, map[string]string{"objectstorage.public-host": "s3.example.test", "platform.tls-mode": "LetsEncrypt"})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: StoreNamespace, Name: "objectstorage-public"}, cert); err != nil {
		t.Fatal(err)
	}
	if issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name"); issuer != "letsencrypt" {
		t.Fatalf("issuer %q", issuer)
	}
}

func TestEndpointReconcilerKeepsRunningOnABadSetting(t *testing.T) {
	_, r := endpointWorld(t, map[string]string{"objectstorage.public-host": "bad host"})
	result, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestBucketStatusShowsThePublicEndpoint(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	rules := Rules{PublicHost: "s3.example.test", MaxBuckets: 100}
	r := bucketReconcilerWith(url, rules)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	if got := readBucket(t, "acme-prod", "data").Status.Endpoint; got != "https://s3.example.test" {
		t.Fatalf("endpoint %q", got)
	}
}

func TestUserSecretShowsThePublicEndpoint(t *testing.T) {
	fresh(t)
	_, iamURL := newFakeIAM(t)
	r := userReconcilerAt(iamURL)
	r.Rules = rulesOf(Rules{PublicHost: "s3.example.test", MaxBuckets: 100})
	readyUser(t, r, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{Permission: "read"})
	secret, found := readSecret(t, "acme-prod", "app-s3")
	if !found || string(secret.Data["Endpoint"]) != "https://s3.example.test" {
		t.Fatalf("secret %+v", secret)
	}
}
