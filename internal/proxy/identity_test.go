package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/cliauth"
)

func sessionRequest(cookie string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, PathSession, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: cookie})
	}
	return req
}

func TestSessionEndpointShape(t *testing.T) {
	expires := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	sessions := stubSessions(func(_ context.Context, cookie string) (Principal, error) {
		groups := []string{"ops"}
		if cookie == "admin" {
			groups = []string{"bedrock-admins"}
		}
		return Principal{
			Subject: "admin", DisplayName: "Administrator", Email: "admin@example.test", Groups: groups,
			Admin: cookie == "admin", Access: "secret-access", CSRF: strings.Repeat("c", 43), ExpiresAt: expires,
		}, nil
	})
	handler := Authenticate(CookieAuth(sessions), SessionHandler(func(context.Context) (string, error) { return "e2e.bedrock.test", nil }))
	rec := do(handler, sessionRequest("admin"))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	user := got["user"].(map[string]any)
	if user["name"] != "admin" || user["displayName"] != "Administrator" || user["email"] != "admin@example.test" ||
		got["admin"] != true || got["host"] != "e2e.bedrock.test" || got["expiresAt"] != "2026-10-09T01:02:03Z" || len(got["csrf"].(string)) != 43 {
		t.Fatalf("body %s", rec.Body.String())
	}
	for _, leaked := range []string{"secret-access", "access", "refresh", "sessionName"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("body holds %q", leaked)
		}
	}
	plain := do(handler, sessionRequest("ops"))
	if !strings.Contains(plain.Body.String(), `"admin":false`) {
		t.Fatalf("non-admin body %s", plain.Body.String())
	}
}

func TestSessionEndpointWithoutCookie(t *testing.T) {
	handler := Authenticate(CookieAuth(aliceSessions()), SessionHandler(labHost))
	rec := do(handler, sessionRequest(""))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"unauthenticated"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionEndpointListsNoGroupsAsEmptyArray(t *testing.T) {
	sessions := stubSessions(func(context.Context, string) (Principal, error) { return Principal{Subject: "bob"}, nil })
	rec := do(Authenticate(CookieAuth(sessions), SessionHandler(labHost)), sessionRequest("any"))
	if !strings.Contains(rec.Body.String(), `"groups":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

const testCACert = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"

func configMapIn(name, ca string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bedrock-system"}, Data: map[string]string{"ca.crt": ca}}
}

func getPath(handler http.Handler, path string) *httptest.ResponseRecorder {
	return do(handler, httptest.NewRequest(http.MethodGet, path, nil))
}

func TestKubeconfigPointsAtTheAPIAndSSO(t *testing.T) {
	handler := KubeconfigHandler(fakeReader(t, configMapIn("kube-root-ca.crt", testCACert)), labHost)
	rec := getPath(handler, PathKubeconfig)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/yaml" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="bedrock.kubeconfig"` {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	loaded, err := clientcmd.Load(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	cluster := loaded.Clusters[loaded.Contexts[loaded.CurrentContext].Cluster]
	exec := loaded.AuthInfos[loaded.Contexts[loaded.CurrentContext].AuthInfo].Exec
	if cluster.Server != "https://api.lab.example:6443" || string(cluster.CertificateAuthorityData) != testCACert ||
		exec.Command != "bedrock" || strings.Join(exec.Args, " ") != "login --server https://sso.lab.example --exec-credential" {
		t.Fatalf("kubeconfig %+v %+v", cluster, exec)
	}
	want, err := cliauth.Kubeconfig("https://api.lab.example:6443", []byte(testCACert), "https://sso.lab.example", "")
	if err != nil || rec.Body.String() != string(want) {
		t.Fatalf("body differs from cliauth.Kubeconfig: %v", err)
	}
}

func TestKubeconfigWithoutClusterCA(t *testing.T) {
	for name, reader := range map[string]client.Reader{
		"empty":   fakeReader(t, configMapIn("kube-root-ca.crt", "")),
		"missing": fakeReader(t),
	} {
		rec := getPath(KubeconfigHandler(reader, labHost), PathKubeconfig)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"upstream_unavailable"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestKubeconfigNeedsNoAuthentication(t *testing.T) {
	handler := KubeconfigHandler(fakeReader(t, configMapIn("kube-root-ca.crt", testCACert)), labHost)
	for _, host := range []string{"console.lab.example", "api.lab.example"} {
		routes := Routes{Console: handler, API: handler, Health: handler}
		if rec := serve(Dispatch(labHost, routes), http.MethodGet, PathKubeconfig, host); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", host, rec.Code)
		}
	}
}

func TestCADownloadServesOnlyTheCertificate(t *testing.T) {
	rec := getPath(CAHandler(fakeReader(t, configMapIn("bedrock-platform-ca", testCACert))), PathCA)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-pem-file" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="bedrock-ca.crt"` ||
		rec.Body.String() != testCACert || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestCADownloadWithoutCA(t *testing.T) {
	for name, reader := range map[string]client.Reader{
		"empty":   fakeReader(t, configMapIn("bedrock-platform-ca", "")),
		"missing": fakeReader(t),
	} {
		rec := getPath(CAHandler(reader), PathCA)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

type secretForbidden struct {
	client.Reader
	t *testing.T
}

func (s secretForbidden) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isSecret := obj.(*corev1.Secret); isSecret {
		s.t.Error("the handler read a Secret")
		return errors.New("secret read")
	}
	return s.Reader.Get(ctx, key, obj, opts...)
}

func TestCAHandlerNeverReadsSecrets(t *testing.T) {
	reader := secretForbidden{Reader: fakeReader(t, configMapIn("bedrock-platform-ca", testCACert)), t: t}
	if rec := getPath(CAHandler(reader), PathCA); rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
}
