package registry

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func fakeCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(&v1alpha1.Addon{}).WithObjects(objects...).Build()
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
