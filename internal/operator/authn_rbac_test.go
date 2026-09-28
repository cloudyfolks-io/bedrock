package operator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func applyManifest(t *testing.T, ctx context.Context, c client.Client, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		if err := c.Create(ctx, &obj); err != nil {
			t.Fatalf("apply %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

func impersonateAs(t *testing.T, cfg *rest.Config, username string, groups []string) client.Client {
	t.Helper()
	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: username, Groups: groups}
	c, err := client.New(impersonated, client.Options{Scheme: newTestScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitForSecretPolicyEnforced(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		probe := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("policy-probe-%d", i), Namespace: "bedrock-system"}}
		err := c.Create(ctx, probe)
		if err == nil {
			_ = c.Delete(ctx, probe)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if apierrors.IsForbidden(err) {
			return
		}
		t.Fatalf("unexpected error waiting for the secret policy to be enforced: %v", err)
	}
	t.Fatal("the secret policy was never enforced by the API server")
}

func labeledAuthnSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "bedrock-system",
			Labels:    map[string]string{v1alpha1.LabelAuthn: "true", v1alpha1.LabelKind: "Credential", v1alpha1.LabelName: name},
		},
		Data: map[string][]byte{"hash": []byte("x")},
	}
}

func TestAuthnSecretPolicy(t *testing.T) {
	admin, cfg := StartTestEnv(t)
	ctx := context.Background()
	for _, name := range []string{"bedrock-system", "other"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	applyManifest(t, ctx, admin, filepath.Join("..", "..", "manifests", "85-authn", "10-rbac.yaml"))
	applyManifest(t, ctx, admin, filepath.Join("..", "..", "manifests", "85-authn", "20-secret-policy.yaml"))

	authn := impersonateAs(t, cfg, "system:serviceaccount:bedrock-system:bedrock-authn", []string{"system:serviceaccounts", "system:serviceaccounts:bedrock-system", "system:authenticated"})
	waitForSecretPolicyEnforced(t, ctx, authn)

	good := labeledAuthnSecret("good")
	if err := authn.Create(ctx, good); err != nil {
		t.Fatalf("a labeled Secret in bedrock-system must be created: %v", err)
	}
	good.Data["hash"] = []byte("y")
	if err := authn.Update(ctx, good); err != nil {
		t.Fatalf("a labeled Secret must stay updatable: %v", err)
	}

	unlabeled := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unlabeled", Namespace: "bedrock-system"}}
	if err := authn.Create(ctx, unlabeled); !apierrors.IsForbidden(err) {
		t.Fatalf("an unlabeled Secret must be refused, got %v", err)
	}

	elsewhere := labeledAuthnSecret("elsewhere")
	elsewhere.Namespace = "other"
	if err := authn.Create(ctx, elsewhere); err == nil {
		t.Fatal("a Secret outside bedrock-system must be refused")
	}

	unlabel := good.DeepCopy()
	delete(unlabel.Labels, v1alpha1.LabelAuthn)
	if err := authn.Update(ctx, unlabel); !apierrors.IsForbidden(err) {
		t.Fatalf("removing the label must be refused, got %v", err)
	}

	if err := authn.Delete(ctx, good); err != nil {
		t.Fatalf("a labeled Secret must stay deletable: %v", err)
	}
}

func TestAgentReadsAuthnFiles(t *testing.T) {
	admin, cfg := StartTestEnv(t)
	ctx := context.Background()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-system"}}); err != nil {
		t.Fatal(err)
	}
	applyManifest(t, ctx, admin, filepath.Join("..", "..", "manifests", "90-bedrock", "agent-rbac.yaml"))
	for _, obj := range []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-authn-apiserver", Namespace: "bedrock-system"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-authn-webhook", Namespace: "bedrock-system"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other-config", Namespace: "bedrock-system"}},
	} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	node := impersonateAs(t, cfg, "system:node:x", []string{"system:nodes", "system:authenticated"})

	var cm corev1.ConfigMap
	if err := node.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "bedrock-authn-apiserver"}, &cm); err != nil {
		t.Fatalf("a node must read the apiserver ConfigMap: %v", err)
	}
	var secret corev1.Secret
	if err := node.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "bedrock-authn-webhook"}, &secret); err != nil {
		t.Fatalf("a node must read the webhook Secret: %v", err)
	}
	var other corev1.ConfigMap
	if err := node.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: "other-config"}, &other); !apierrors.IsForbidden(err) {
		t.Fatalf("a node must not read any other object in bedrock-system, got %v", err)
	}
}
