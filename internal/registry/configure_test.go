package registry

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const adminPassword = "harbor-admin-password-value"

type harborAnswer struct {
	status int
	body   string
	header map[string]string
}

type harborCall struct {
	method string
	uri    string
	body   string
}

type harborFake struct {
	mu      sync.Mutex
	answers map[string]harborAnswer
	calls   []harborCall
	server  *httptest.Server
}

func newHarborFake(t *testing.T, answers map[string]harborAnswer) *harborFake {
	t.Helper()
	fakeHarbor := &harborFake{answers: answers}
	fakeHarbor.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fakeHarbor.mu.Lock()
		fakeHarbor.calls = append(fakeHarbor.calls, harborCall{method: r.Method, uri: r.RequestURI, body: string(raw)})
		answer, found := fakeHarbor.answers[r.Method+" "+r.RequestURI]
		fakeHarbor.mu.Unlock()
		if !found {
			answer = harborAnswer{status: http.StatusOK, body: "{}"}
		}
		for key, value := range answer.header {
			w.Header().Set(key, value)
		}
		if answer.status != 0 {
			w.WriteHeader(answer.status)
		}
		_, _ = io.WriteString(w, answer.body)
	}))
	t.Cleanup(fakeHarbor.server.Close)
	return fakeHarbor
}

func (f *harborFake) sequence() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, call.method+" "+call.uri)
	}
	return out
}

func (f *harborFake) bodyOf(method, uri string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.method == method && call.uri == uri {
			return call.body
		}
	}
	return ""
}

func healthy() map[string]harborAnswer {
	return map[string]harborAnswer{"GET /api/v2.0/health": {body: `{"status":"healthy"}`}}
}

func adminSecretObject() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "harbor-admin"},
		Data:       map[string][]byte{"HARBOR_ADMIN_PASSWORD": []byte(adminPassword)},
	}
}

func harborReconciler(t *testing.T, fakeHarbor *harborFake, objects ...client.Object) (*HarborReconciler, client.Client) {
	t.Helper()
	all := append([]client.Object{addonObject("registry", true), adminSecretObject()}, objects...)
	c := fakeCluster(t, all...)
	return &HarborReconciler{Client: c, BaseURL: fakeHarbor.server.URL}, c
}

func configured(t *testing.T, c client.Client) (string, string, metav1.ConditionStatus) {
	t.Helper()
	var addon = addonObject("registry", true)
	if err := c.Get(context.Background(), client.ObjectKey{Name: "registry"}, addon); err != nil {
		t.Fatal(err)
	}
	for _, condition := range addon.Status.Conditions {
		if condition.Type == ConditionConfigured {
			return condition.Reason, condition.Message, condition.Status
		}
	}
	t.Fatal("no HarborConfigured condition")
	return "", "", ""
}

const twoCaches = `[{"name":"mirror-docker","type":"docker-hub","url":"https://hub.docker.com"},{"name":"mirror-ghcr","type":"github-ghcr","url":"https://ghcr.io"}]`

func TestHarborIsConfiguredInOrder(t *testing.T) {
	answers := healthy()
	answers["POST /api/v2.0/registries"] = harborAnswer{status: 201, header: map[string]string{"Location": "/api/v2.0/registries/8"}}
	fakeHarbor := newHarborFake(t, answers)
	r, c := harborReconciler(t, fakeHarbor, setting("registry.proxy-caches", twoCaches))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v2.0/health",
		"PUT /api/v2.0/configurations",
		"POST /api/v2.0/system/gc/schedule",
		"POST /api/v2.0/registries",
		"POST /api/v2.0/projects",
		"POST /api/v2.0/registries",
		"POST /api/v2.0/projects",
	}
	if got := fakeHarbor.sequence(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
	reason, _, status := configured(t, c)
	if reason != "Configured" || status != metav1.ConditionTrue {
		t.Errorf("condition %s %s", reason, status)
	}
}

func TestHarborConfigurationBodyGolden(t *testing.T) {
	fakeHarbor := newHarborFake(t, healthy())
	r, _ := harborReconciler(t, fakeHarbor)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if got := fakeHarbor.bodyOf("PUT", "/api/v2.0/configurations"); got != `{"auth_mode":"db_auth","project_creation_restriction":"adminonly","robot_name_prefix":"robot$","self_registration":false}` {
		t.Errorf("configuration %s", got)
	}
	if got := fakeHarbor.bodyOf("POST", "/api/v2.0/system/gc/schedule"); !strings.Contains(got, `"cron":"0 0 3 * * *"`) {
		t.Errorf("gc %s", got)
	}
}

func TestChangedSettingsReachHarbor(t *testing.T) {
	fakeHarbor := newHarborFake(t, healthy())
	r, _ := harborReconciler(t, fakeHarbor, setting("registry.robot-prefix", "key_"), setting("registry.gc-cron", "0 30 1 * * *"))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if got := fakeHarbor.bodyOf("PUT", "/api/v2.0/configurations"); !strings.Contains(got, `"robot_name_prefix":"key_"`) {
		t.Errorf("configuration %s", got)
	}
	if got := fakeHarbor.bodyOf("POST", "/api/v2.0/system/gc/schedule"); !strings.Contains(got, `"cron":"0 30 1 * * *"`) {
		t.Errorf("gc %s", got)
	}
}

func TestAChangedCacheListIsApplied(t *testing.T) {
	answers := healthy()
	answers["POST /api/v2.0/registries"] = harborAnswer{status: 201, header: map[string]string{"Location": "/api/v2.0/registries/8"}}
	fakeHarbor := newHarborFake(t, answers)
	r, c := harborReconciler(t, fakeHarbor, setting("registry.proxy-caches", `[{"name":"mirror-docker","type":"docker-hub","url":"https://hub.docker.com"}]`))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	var list = setting("registry.proxy-caches", "")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "registry.proxy-caches"}, list); err != nil {
		t.Fatal(err)
	}
	list.Spec.Value = twoCaches
	if err := c.Update(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if got := fakeHarbor.bodyOf("POST", "/api/v2.0/projects"); !strings.Contains(got, "mirror-docker") {
		t.Errorf("projects %s", got)
	}
	count := 0
	for _, call := range fakeHarbor.sequence() {
		if call == "POST /api/v2.0/registries" {
			count++
		}
	}
	if count != 3 {
		t.Errorf("registry endpoint calls %d, want 3 (one, then two)", count)
	}
}

func TestAConflictIsSuccess(t *testing.T) {
	answers := healthy()
	answers["POST /api/v2.0/registries"] = harborAnswer{status: 409}
	answers["POST /api/v2.0/projects"] = harborAnswer{status: 409}
	answers["GET /api/v2.0/registries?page=1&page_size=100&q=name%3Dmirror-docker"] = harborAnswer{body: `[{"id":8,"name":"mirror-docker","type":"docker-hub"}]`}
	fakeHarbor := newHarborFake(t, answers)
	r, c := harborReconciler(t, fakeHarbor, setting("registry.proxy-caches", `[{"name":"mirror-docker","type":"docker-hub","url":"https://hub.docker.com"}]`))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if reason, message, _ := configured(t, c); reason != "Configured" {
		t.Errorf("condition %s %s", reason, message)
	}
}

func TestATypeMismatchFailsWithAClearMessage(t *testing.T) {
	answers := healthy()
	answers["POST /api/v2.0/registries"] = harborAnswer{status: 409}
	answers["GET /api/v2.0/registries?page=1&page_size=100&q=name%3Dmirror-docker"] = harborAnswer{body: `[{"id":8,"name":"mirror-docker","type":"github-ghcr"}]`}
	fakeHarbor := newHarborFake(t, answers)
	r, c := harborReconciler(t, fakeHarbor, setting("registry.proxy-caches", `[{"name":"mirror-docker","type":"docker-hub","url":"https://hub.docker.com"}]`))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	reason, message, status := configured(t, c)
	if reason != "ProxyCacheTypeMismatch" || status != metav1.ConditionFalse || !strings.Contains(message, "mirror-docker") || !strings.Contains(message, "github-ghcr") || !strings.Contains(message, "docker-hub") {
		t.Errorf("condition %s %s %s", reason, status, message)
	}
	if slices.Contains(fakeHarbor.sequence(), "POST /api/v2.0/projects") {
		t.Error("a project was made for a mismatching endpoint")
	}
}

func TestOneMismatchDoesNotBlockTheOtherCaches(t *testing.T) {
	answers := healthy()
	answers["POST /api/v2.0/registries"] = harborAnswer{status: 409}
	answers["GET /api/v2.0/registries?page=1&page_size=100&q=name%3Dmirror-docker"] = harborAnswer{body: `[{"id":8,"name":"mirror-docker","type":"quay"}]`}
	answers["GET /api/v2.0/registries?page=1&page_size=100&q=name%3Dmirror-ghcr"] = harborAnswer{body: `[{"id":9,"name":"mirror-ghcr","type":"github-ghcr"}]`}
	fakeHarbor := newHarborFake(t, answers)
	r, _ := harborReconciler(t, fakeHarbor, setting("registry.proxy-caches", twoCaches))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if got := fakeHarbor.bodyOf("POST", "/api/v2.0/projects"); !strings.Contains(got, "mirror-ghcr") {
		t.Errorf("the second cache was not made: %s", got)
	}
}

func TestAnUnhealthyHarborWaits(t *testing.T) {
	fakeHarbor := newHarborFake(t, map[string]harborAnswer{"GET /api/v2.0/health": {status: 503, body: `{"errors":[{"message":"starting"}]}`}})
	r, c := harborReconciler(t, fakeHarbor)
	got, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatal(err)
	}
	reason, _, status := configured(t, c)
	if reason != "HarborNotReady" || status != metav1.ConditionFalse || got.RequeueAfter != failedRequeue {
		t.Errorf("condition %s %s requeue %s", reason, status, got.RequeueAfter)
	}
	if calls := fakeHarbor.sequence(); len(calls) != 1 {
		t.Errorf("calls %v", calls)
	}
}

func TestWithoutTheAdminSecretNothingIsCalled(t *testing.T) {
	fakeHarbor := newHarborFake(t, healthy())
	c := fakeCluster(t, addonObject("registry", true))
	r := &HarborReconciler{Client: c, BaseURL: fakeHarbor.server.URL}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if reason, _, _ := configured(t, c); reason != "AdminSecretMissing" {
		t.Errorf("reason %s", reason)
	}
	if len(fakeHarbor.sequence()) != 0 {
		t.Errorf("calls %v", fakeHarbor.sequence())
	}
}

func TestADisabledAddonIsNotConfigured(t *testing.T) {
	fakeHarbor := newHarborFake(t, healthy())
	c := fakeCluster(t, addonObject("registry", false), adminSecretObject())
	r := &HarborReconciler{Client: c, BaseURL: fakeHarbor.server.URL}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if len(fakeHarbor.sequence()) != 0 {
		t.Errorf("calls %v", fakeHarbor.sequence())
	}
}

func TestTheAdminPasswordIsNeverLoggedOrReported(t *testing.T) {
	answers := healthy()
	answers["PUT /api/v2.0/configurations"] = harborAnswer{status: 400, body: `{"errors":[{"message":"bad value, password ` + adminPassword + `"}]}`}
	fakeHarbor := newHarborFake(t, answers)
	r, c := harborReconciler(t, fakeHarbor)
	var logs bytes.Buffer
	sink := funcr.New(func(prefix, args string) { logs.WriteString(prefix + args + "\n") }, funcr.Options{Verbosity: 9})
	ctx := logr.NewContext(context.Background(), sink)
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	reason, message, _ := configured(t, c)
	if reason != "ConfigurationFailed" {
		t.Fatalf("reason %s", reason)
	}
	if strings.Contains(message, adminPassword) || strings.Contains(logs.String(), adminPassword) {
		t.Errorf("the password leaked: message %q logs %q", message, logs.String())
	}
}

func TestInvalidHarborSettingsAreReported(t *testing.T) {
	fakeHarbor := newHarborFake(t, healthy())
	r, c := harborReconciler(t, fakeHarbor, setting("registry.gc-cron", "bad"))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if reason, _, _ := configured(t, c); reason != "InvalidSettings" {
		t.Errorf("reason %s", reason)
	}
	if len(fakeHarbor.sequence()) != 0 {
		t.Errorf("calls %v", fakeHarbor.sequence())
	}
}

func TestTheRequeueIsTenMinutesWhenConfigured(t *testing.T) {
	fakeHarbor := newHarborFake(t, healthy())
	r, _ := harborReconciler(t, fakeHarbor)
	got, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil || got.RequeueAfter.Minutes() != 10 {
		t.Errorf("result %+v %v", got, err)
	}
}
