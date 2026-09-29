package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

func TestTokenUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"token"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("token create")) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestTokenCreateRequiresRoles(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"token", "create"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("--roles")) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestBuildTokenCarriesMirror(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName},
		Spec:       v1alpha1.ClusterSpec{DesiredVersion: "v0.1.0-test", API: v1alpha1.APISpec{VIP: "10.0.10.10"}, Registry: v1alpha1.RegistrySpec{Mirror: "https://m.example"}},
		Status:     v1alpha1.ClusterStatus{Version: "v0.1.0-test"},
	}
	rel := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.1.0-test"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.1.0-test", K0sVersion: "v1.36.3+k0s.0"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, rel).WithStatusSubresource(cluster).Build()
	e := &host.FakeExec{Responses: map[string]string{"/usr/local/bin/k0s token create --role worker --expiry 1h": "tok\n"}}
	token, err := buildToken(context.Background(), c, k0s.Client{Exec: e, Binary: "/usr/local/bin/k0s"}, []string{v1alpha1.RoleWorkload}, "1h", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if token.Mirror != "https://m.example" {
		t.Fatalf("token %+v", token)
	}
}

func tokenClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName},
		Spec:       v1alpha1.ClusterSpec{DesiredVersion: "v0.1.0-test", API: v1alpha1.APISpec{VIP: "10.0.10.10"}},
		Status:     v1alpha1.ClusterStatus{Version: "v0.1.0-test"},
	}
	rel := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.1.0-test"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.1.0-test", K0sVersion: "v1.36.3+k0s.0"}}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, rel).WithStatusSubresource(cluster).Build()
}

func TestControllerTokenCarriesAuthnFiles(t *testing.T) {
	c := tokenClient(t)
	dir := t.TempDir()
	k0sConfig := filepath.Join(dir, "k0s.yaml")
	authnDir := filepath.Join(dir, "authn")
	if err := os.MkdirAll(authnDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{k0sConfig: "apiVersion: k0s.k0sproject.io/v1beta1\n", filepath.Join(authnDir, "authentication.yaml"): "a", filepath.Join(authnDir, "webhook.kubeconfig"): "w"} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := &host.FakeExec{Responses: map[string]string{
		"/usr/local/bin/k0s token create --role controller --expiry 1h": "tok\n",
		"/usr/local/bin/k0s token create --role worker --expiry 1h":     "tok\n",
	}}
	k0sClient := k0s.Client{Exec: e, Binary: "/usr/local/bin/k0s"}
	token, err := buildToken(context.Background(), c, k0sClient, []string{v1alpha1.RoleControlPlane}, "1h", k0sConfig, authnDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(token.AuthnFiles["authentication.yaml"]) != "a" || string(token.AuthnFiles["webhook.kubeconfig"]) != "w" || len(token.AuthnFiles) != 2 {
		t.Fatalf("authn files %v", token.AuthnFiles)
	}
	worker, err := buildToken(context.Background(), c, k0sClient, []string{v1alpha1.RoleWorkload}, "1h", "", authnDir)
	if err != nil {
		t.Fatal(err)
	}
	if worker.AuthnFiles != nil {
		t.Fatal("a worker token carries no authn files")
	}
	if _, err := buildToken(context.Background(), c, k0sClient, []string{v1alpha1.RoleControlPlane}, "1h", k0sConfig, t.TempDir()); err == nil || !strings.Contains(err.Error(), "authentication.yaml") {
		t.Fatalf("a controller without the files cannot make a controller token: %v", err)
	}
}
