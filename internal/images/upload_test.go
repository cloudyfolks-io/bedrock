package images

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

type tokenRecorder struct {
	mu       sync.Mutex
	requests []*unstructured.Unstructured
	failWith error
}

func (r *tokenRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func uploadClient(t *testing.T, recorder *tokenRecorder, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			request, ok := obj.(*unstructured.Unstructured)
			if !ok || request.GetKind() != "UploadTokenRequest" {
				return c.Create(ctx, obj, opts...)
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			if recorder.failWith != nil {
				return recorder.failWith
			}
			recorder.requests = append(recorder.requests, request.DeepCopy())
			return unstructured.SetNestedField(request.Object, "tok-123", "status", "token")
		},
	}).Build()
}

func uploadImage(phase string) *v1alpha1.Image {
	return &v1alpha1.Image{
		ObjectMeta: metav1.ObjectMeta{Name: "disk", Namespace: "acme-web"},
		Spec:       v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}},
		Status:     v1alpha1.ImageStatus{Phase: phase},
	}
}

func hostSetting(host string) *v1alpha1.Setting {
	return &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.host"}, Spec: v1alpha1.SettingSpec{Value: host}}
}

func callUpload(ctx context.Context, c client.Client, method string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler := UploadHandler(func() time.Time { return testNow })
	handler(ctx, c, client.ObjectKey{Namespace: "acme-web", Name: "disk"}, recorder, httptest.NewRequest(method, "/upload", nil))
	return recorder
}

func errorOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body["error"]
}

func TestUploadReturnsTheSignedURL(t *testing.T) {
	recorder := &tokenRecorder{}
	c := uploadClient(t, recorder, uploadImage("WaitingForUpload"), hostSetting("cloud.example.com"))
	response := callUpload(context.Background(), c, http.MethodPost)

	if response.Code != http.StatusOK {
		t.Fatalf("status %d %s", response.Code, response.Body)
	}
	var body UploadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := UploadResponse{URL: "https://upload.cloud.example.com/v1beta1/upload-async", Token: "tok-123", ExpiresAt: testNow.Add(5 * time.Minute)}
	if body.URL != want.URL || body.Token != want.Token || !body.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("body %+v", body)
	}
	if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", response.Header())
	}
	if recorder.count() != 1 {
		t.Fatalf("token requests %d", recorder.count())
	}
	request := recorder.requests[0]
	if request.GetNamespace() != "acme-web" || request.GetName() != "disk-import" || nested(t, request, "spec", "pvcName") != "disk-import" {
		t.Fatalf("request %v", request.Object)
	}
}

func TestUploadRefusesOtherMethods(t *testing.T) {
	recorder := &tokenRecorder{}
	c := uploadClient(t, recorder, uploadImage("WaitingForUpload"), hostSetting("cloud.example.com"))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		response := callUpload(context.Background(), c, method)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "POST" {
			t.Fatalf("%s: %d %v", method, response.Code, response.Header())
		}
	}
	if recorder.count() != 0 {
		t.Fatal("no token request expected")
	}
}

func TestUploadRefusesAnImageThatIsNotWaiting(t *testing.T) {
	for _, phase := range []string{"Pending", "Importing", "Ready", "Failed"} {
		recorder := &tokenRecorder{}
		c := uploadClient(t, recorder, uploadImage(phase), hostSetting("cloud.example.com"))
		response := callUpload(context.Background(), c, http.MethodPost)
		want := "image is " + phase + ", not WaitingForUpload"
		if response.Code != http.StatusConflict || errorOf(t, response) != want || recorder.count() != 0 {
			t.Fatalf("%s: %d %s", phase, response.Code, response.Body)
		}
	}
}

func TestUploadRefusesNonUploadSources(t *testing.T) {
	recorder := &tokenRecorder{}
	img := uploadImage("WaitingForUpload")
	img.Spec.Source = httpSpec("https://example.com/a.qcow2").Source
	c := uploadClient(t, recorder, img, hostSetting("cloud.example.com"))
	response := callUpload(context.Background(), c, http.MethodPost)
	if response.Code != http.StatusConflict || errorOf(t, response) != "image has no upload source" || recorder.count() != 0 {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}

func TestUploadNeedsThePlatformHost(t *testing.T) {
	recorder := &tokenRecorder{}
	c := uploadClient(t, recorder, uploadImage("WaitingForUpload"))
	response := callUpload(context.Background(), c, http.MethodPost)
	if response.Code != http.StatusServiceUnavailable || errorOf(t, response) != "platform.host is not set" || recorder.count() != 0 {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}

func TestUploadUnknownImage(t *testing.T) {
	c := uploadClient(t, &tokenRecorder{}, hostSetting("cloud.example.com"))
	response := callUpload(context.Background(), c, http.MethodPost)
	if response.Code != http.StatusNotFound || errorOf(t, response) != "image not found" {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}

func TestUploadReportsAnUploadServiceFailure(t *testing.T) {
	recorder := &tokenRecorder{failWith: errors.New("webhook denied")}
	c := uploadClient(t, recorder, uploadImage("WaitingForUpload"), hostSetting("cloud.example.com"))
	response := callUpload(context.Background(), c, http.MethodPost)
	if response.Code != http.StatusBadGateway || errorOf(t, response) != "the upload service refused the request" {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}

func TestUploadNeverStoresOrLogsTheToken(t *testing.T) {
	recorder := &tokenRecorder{}
	c := uploadClient(t, recorder, uploadImage("WaitingForUpload"), hostSetting("cloud.example.com"))
	var logged strings.Builder
	ctx := log.IntoContext(context.Background(), zap.New(zap.WriteTo(&logged), zap.UseDevMode(true)))

	if response := callUpload(ctx, c, http.MethodPost); response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
	var stored v1alpha1.Image
	if err := c.Get(ctx, client.ObjectKey{Namespace: "acme-web", Name: "disk"}, &stored); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "tok-123") || strings.Contains(logged.String(), "tok-123") {
		t.Fatalf("the token leaked: %s %s", content, logged.String())
	}
}

func TestRegisterSubresources(t *testing.T) {
	reg := proxy.NewSubresources()
	if err := RegisterSubresources(reg); err != nil {
		t.Fatal(err)
	}
	if _, found := reg.Lookup("images", "upload"); !found {
		t.Fatal("the upload handler must be registered")
	}
	if err := RegisterSubresources(reg); !errors.Is(err, proxy.ErrDuplicateSubresource) {
		t.Fatalf("second registration: %v", err)
	}
}
