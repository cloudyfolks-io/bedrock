package cli

import (
	"bytes"
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	token, err := buildToken(context.Background(), c, k0s.Client{Exec: e, Binary: "/usr/local/bin/k0s"}, []string{v1alpha1.RoleWorkload}, "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if token.Mirror != "https://m.example" {
		t.Fatalf("token %+v", token)
	}
}
