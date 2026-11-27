package registry

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func fakeCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(&v1alpha1.Addon{}, &v1alpha1.Registry{}, &v1alpha1.RegistryCredential{}).WithInterceptorFuncs(interceptor.Funcs{Apply: applyNetworkPolicies}).WithObjects(objects...).Build()
}

func repeating(b byte) io.Reader {
	return bytes.NewReader(bytes.Repeat([]byte{b}, 1<<16))
}

func seeded(seed byte) io.Reader {
	var key [32]byte
	key[0] = seed
	return rand.NewChaCha8(key)
}

func names(objects []*unstructured.Unstructured) []string {
	out := make([]string, 0, len(objects))
	for _, obj := range objects {
		out = append(out, obj.GetKind()+"/"+obj.GetName())
	}
	sort.Strings(out)
	return out
}

func addonObject(name string, enabled bool) *v1alpha1.Addon {
	return &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AddonSpec{Enabled: enabled}}
}

func setting(name, value string) *v1alpha1.Setting {
	return &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.SettingSpec{Value: value}}
}

func getSecret(t *testing.T, c client.Client, name string) corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: name}, &secret); err != nil {
		t.Fatalf("secret %s: %v", name, err)
	}
	return secret
}

func fakeS3(t *testing.T, status int, code string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "<Error><Code>"+code+"</Code></Error>")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func applyNetworkPolicies(ctx context.Context, c client.WithWatch, configuration runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	content, ok := configuration.(runtime.Unstructured)
	if !ok || !slices.Contains([]any{"NetworkPolicy", "Role", "RoleBinding"}, content.UnstructuredContent()["kind"]) {
		return c.Apply(ctx, configuration, opts...)
	}
	policy := &unstructured.Unstructured{Object: content.UnstructuredContent()}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(policy.GroupVersionKind())
	err := c.Get(ctx, client.ObjectKeyFromObject(policy), existing)
	if err == nil {
		if err := c.Delete(ctx, existing); err != nil {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if typed := typedRBAC(policy); typed != nil {
		return c.Create(ctx, typed)
	}
	return c.Create(ctx, policy)
}

func typedRBAC(obj *unstructured.Unstructured) client.Object {
	var typed client.Object
	switch obj.GetKind() {
	case "Role":
		typed = &rbacv1.Role{}
	case "RoleBinding":
		typed = &rbacv1.RoleBinding{}
	default:
		return nil
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, typed); err != nil {
		return nil
	}
	return typed
}
