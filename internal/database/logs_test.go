package database

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

type logCall struct {
	namespace string
	pod       string
	container string
	tail      int64
}

type logRecorder struct {
	calls []logCall
	body  string
	err   error
}

func (r *logRecorder) source(_ context.Context, namespace, pod, container string, tail int64) (io.ReadCloser, error) {
	r.calls = append(r.calls, logCall{namespace, pod, container, tail})
	if r.err != nil {
		return nil, r.err
	}
	return io.NopCloser(strings.NewReader(r.body)), nil
}

func logPod(name, instance string, ready bool) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PlatformNamespace, Labels: map[string]string{
		"app.kubernetes.io/instance": instance, "app.kubernetes.io/component": "engine", "cnpg.io/cluster": instance, "cnpg.io/instanceRole": "primary",
	}}}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	return pod
}

func logsClient(t *testing.T, engineName string, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	must(t, v1alpha1.AddToScheme(scheme))
	db := &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "acme-web", UID: types.UID("uid-1")},
		Spec:       v1alpha1.DatabaseSpec{Engine: engineName, Version: "17", Class: "small", StorageGi: 10},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, db)...).Build()
}

func callLogs(handler proxy.SubresourceHandler, c client.Client, name, method, query string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/logs?"+query, nil)
	handler(context.Background(), c, client.ObjectKey{Namespace: "acme-web", Name: name}, recorder, request)
	return recorder
}

func TestLogsReadTheEngineContainerOfThePrimaryPod(t *testing.T) {
	cases := map[string]string{
		v1alpha1.DatabaseEnginePostgreSQL: "postgres",
		v1alpha1.DatabaseEngineMySQL:      "mysql",
		v1alpha1.DatabaseEngineValkey:     "valkey",
	}
	for kind, container := range cases {
		recorder := &logRecorder{body: "line one\nline two\n"}
		pod := logPod("uid-1-1", "uid-1", true)
		if kind == v1alpha1.DatabaseEngineMySQL {
			pod.Labels = map[string]string{"app.kubernetes.io/instance": "uid-1", "app.kubernetes.io/component": "mysql", "mysql.percona.com/primary": "true"}
		}
		response := callLogs(Logs(recorder.source), logsClient(t, kind, pod), "orders", http.MethodGet, "")
		if response.Code != http.StatusOK || response.Body.String() != "line one\nline two\n" || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %q %v", kind, response.Code, response.Body.String(), response.Header())
		}
		if len(recorder.calls) != 1 || recorder.calls[0] != (logCall{PlatformNamespace, "uid-1-1", container, 200}) {
			t.Errorf("%s: calls %+v", kind, recorder.calls)
		}
	}
}

func TestLogsTailDefaultAndMaximum(t *testing.T) {
	cases := []struct {
		query string
		tail  int64
	}{{"", 200}, {"tail=50", 50}, {"tail=500", 500}, {"tail=501", 500}, {"tail=100000", 500}, {"tail=0", 200}, {"tail=-5", 200}, {"tail=1", 1}}
	for _, c := range cases {
		recorder := &logRecorder{body: "x\n"}
		callLogs(Logs(recorder.source), logsClient(t, "postgresql", logPod("uid-1-1", "uid-1", true)), "orders", http.MethodGet, c.query)
		if len(recorder.calls) != 1 || recorder.calls[0].tail != c.tail {
			t.Errorf("%q: calls %+v, want tail %d", c.query, recorder.calls, c.tail)
		}
	}
	recorder := &logRecorder{}
	response := callLogs(Logs(recorder.source), logsClient(t, "postgresql", logPod("uid-1-1", "uid-1", true)), "orders", http.MethodGet, "tail=many")
	if response.Code != http.StatusBadRequest || len(recorder.calls) != 0 {
		t.Fatalf("a bad tail: %d calls %d", response.Code, len(recorder.calls))
	}
}

func TestLogsOutputIsCapped(t *testing.T) {
	recorder := &logRecorder{body: strings.Repeat("a", MaxLogBytes+4096)}
	response := callLogs(Logs(recorder.source), logsClient(t, "postgresql", logPod("uid-1-1", "uid-1", true)), "orders", http.MethodGet, "")
	if response.Code != http.StatusOK || response.Body.Len() != MaxLogBytes {
		t.Fatalf("status %d length %d", response.Code, response.Body.Len())
	}
}

func TestLogsWithoutAReadyPod(t *testing.T) {
	for name, pods := range map[string][]client.Object{
		"no pod":                  nil,
		"pod not ready":           {logPod("uid-1-1", "uid-1", false)},
		"pod of another database": {logPod("uid-2-1", "uid-2", true)},
	} {
		recorder := &logRecorder{}
		response := callLogs(Logs(recorder.source), logsClient(t, "postgresql", pods...), "orders", http.MethodGet, "")
		if response.Code != http.StatusConflict || len(recorder.calls) != 0 {
			t.Errorf("%s: status %d calls %d", name, response.Code, len(recorder.calls))
		}
	}
}

func TestLogsPicksTheReadyPodOfTheDatabaseOnly(t *testing.T) {
	recorder := &logRecorder{body: "x\n"}
	objects := []client.Object{logPod("uid-1-1", "uid-1", false), logPod("uid-1-2", "uid-1", true), logPod("uid-2-1", "uid-2", true)}
	callLogs(Logs(recorder.source), logsClient(t, "postgresql", objects...), "orders", http.MethodGet, "")
	if len(recorder.calls) != 1 || recorder.calls[0].pod != "uid-1-2" {
		t.Fatalf("calls %+v", recorder.calls)
	}
}

func TestLogsOfAMissingDatabaseAndOtherMethods(t *testing.T) {
	recorder := &logRecorder{}
	c := logsClient(t, "postgresql", logPod("uid-1-1", "uid-1", true))
	if response := callLogs(Logs(recorder.source), c, "ghost", http.MethodGet, ""); response.Code != http.StatusNotFound {
		t.Fatalf("missing database: %d", response.Code)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response := callLogs(Logs(recorder.source), c, "orders", method, "")
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s: %d %v", method, response.Code, response.Header())
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("calls %+v", recorder.calls)
	}
}

func TestLogsOfAnUnreachablePodIsABadGateway(t *testing.T) {
	recorder := &logRecorder{err: io.ErrUnexpectedEOF}
	response := callLogs(Logs(recorder.source), logsClient(t, "postgresql", logPod("uid-1-1", "uid-1", true)), "orders", http.MethodGet, "")
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "unexpected EOF") {
		t.Fatalf("the error text leaks: %s", response.Body.String())
	}
}

func TestRegisterLogsAddsTheLogsSubresourceOfDatabases(t *testing.T) {
	registry := proxy.NewSubresources()
	recorder := &logRecorder{}
	if err := registerWith(registry, recorder.source); err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Lookup("databases", "logs"); !found {
		t.Fatal("databases/logs is not registered")
	}
	if err := registerWith(registry, recorder.source); err == nil {
		t.Fatal("a second registration was accepted")
	}
}

func TestProxyRulesReadDatabases(t *testing.T) {
	rules := ProxyRules()
	if len(rules) != 1 || rules[0].APIGroups[0] != "bedrock.cloudyfolks.io" || rules[0].Resources[0] != "databases" || len(rules[0].Verbs) != 1 || rules[0].Verbs[0] != "get" {
		t.Fatalf("rules %+v", rules)
	}
}
