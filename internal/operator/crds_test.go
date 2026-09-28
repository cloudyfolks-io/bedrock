package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestCRDsRoundTrip(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.host"}, Spec: v1alpha1.SettingSpec{Value: "lab.example"}}
	if err := c.Create(ctx, setting); err != nil {
		t.Fatal(err)
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	bad := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}, Spec: v1alpha1.HostSpec{Roles: []string{"storage"}}}
	if err := c.Create(ctx, bad); err == nil {
		t.Fatal("invalid role must be rejected by the CRD schema")
	}
	release := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.1.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.1.0", Image: "ghcr.io/cloudyfolks-labs/bedrock/release:v0.1.0", K0sVersion: "v1.36.3+k0s.0"}}
	if err := c.Create(ctx, release); err != nil {
		t.Fatal(err)
	}
	release.Spec.K0sVersion = "v1.36.4+k0s.0"
	if err := c.Update(ctx, release); err == nil {
		t.Fatal("release spec must be immutable")
	}

	var got v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: "platform.host"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Value != "lab.example" {
		t.Fatalf("value %q", got.Spec.Value)
	}
}
