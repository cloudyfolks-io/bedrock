package objectstorage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

type fakeIAM struct {
	mu       sync.Mutex
	policies map[string]string
	actions  []string
}

func newFakeIAM(t *testing.T) (*fakeIAM, string) {
	t.Helper()
	iam := &fakeIAM{policies: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(iam.serve))
	t.Cleanup(server.Close)
	return iam, server.URL
}

func (f *fakeIAM) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	f.mu.Lock()
	defer f.mu.Unlock()
	user := form.Get("UserName")
	f.actions = append(f.actions, form.Get("Action")+" "+user)
	switch form.Get("Action") {
	case "PutUserPolicy":
		f.policies[user] = form.Get("PolicyDocument")
	case "GetUserPolicy":
		document, found := f.policies[user]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<ErrorResponse><Error><Code>NoSuchEntity</Code></Error></ErrorResponse>`)
			return
		}
		io.WriteString(w, `<GetUserPolicyResponse><GetUserPolicyResult><PolicyDocument>`+strings.ReplaceAll(document, "&", "&amp;")+`</PolicyDocument></GetUserPolicyResult></GetUserPolicyResponse>`)
	case "DeleteUserPolicy":
		delete(f.policies, user)
	}
}

func (f *fakeIAM) policy(user string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policies[user]
}

func (f *fakeIAM) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	taken := f.actions
	f.actions = nil
	return taken
}

func fixedClient() *s3.Client {
	return &s3.Client{Now: func() time.Time { return time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC) }}
}

func userReconcilerAt(url string) *UserReconciler {
	return &UserReconciler{Client: testClient, S3: fixedClient(), Endpoint: url}
}

func createRookKeys(t *testing.T, rgwUser string, data map[string][]byte) {
	t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialsSecretName(rgwUser), Namespace: StoreNamespace}, Data: data}
	if err := testClient.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
}

func fakeRookKeys() map[string][]byte {
	return map[string][]byte{"AccessKey": []byte("AK"), "SecretKey": []byte("SK")}
}

func readyAccountOf(t *testing.T, namespace string) {
	t.Helper()
	project := loadedProject(t, namespace)
	if _, _, err := EnsureAccount(context.Background(), testClient, project); err != nil {
		t.Fatal(err)
	}
	makeAccountReady(t, namespace)
	createRootSecret(t, namespace)
}

func getRookUser(t *testing.T, name string) (*unstructured.Unstructured, bool) {
	t.Helper()
	obj, found, err := getRook(context.Background(), testClient, cephObjectStoreUserGVK, client.ObjectKey{Namespace: StoreNamespace, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return obj, found
}

func makeRookUserReady(t *testing.T, name string) {
	t.Helper()
	rgw, found := getRookUser(t, name)
	if !found {
		t.Fatalf("no Rook user %s", name)
	}
	setRookPhase(t, rgw, "Ready")
	createRookKeys(t, name, fakeRookKeys())
}

func readyUser(t *testing.T, r *UserReconciler, namespace, name string, spec v1alpha1.ObjectStorageUserSpec) {
	t.Helper()
	createStore(t, "Ready")
	createUser(t, namespace, name, spec)
	reconcileUserWith(t, r, namespace, name)
	readyAccountOf(t, namespace)
	reconcileUserWith(t, r, namespace, name)
	makeRookUserReady(t, RGWUserName(namespace, name))
	reconcileUserWith(t, r, namespace, name)
}
