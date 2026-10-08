package compute

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type recordedRequest struct {
	Method, Path, Authorization, Cookie string
}

type fakeKubeVirt struct {
	mu       sync.Mutex
	requests []recordedRequest
	Server   *httptest.Server
}

func newFakeKubeVirt(t *testing.T) *fakeKubeVirt {
	t.Helper()
	fake := &fakeKubeVirt{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.requests = append(fake.requests, recordedRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie")})
		fake.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(fake.Server.Close)
	return fake
}

func (f *fakeKubeVirt) seen() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func actionDeps(fake *fakeKubeVirt) Deps {
	return Deps{
		Config: &rest.Config{Host: fake.Server.URL, BearerToken: "proxy-token"},
		Now:    func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) },
	}
}

func actionVM(t *testing.T, c client.Client, phase string) client.ObjectKey {
	t.Helper()
	ns := uniqueNamespace(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	vm := getVM(t, c, ns, "web-1")
	vm.Status.Phase = phase
	must(t, c.Status().Update(context.Background(), &vm))
	return client.ObjectKey{Namespace: ns, Name: "web-1"}
}

func requireActionError(t *testing.T, err error, code int) {
	t.Helper()
	var actionErr ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != code {
		t.Fatalf("error %v, want code %d", err, code)
	}
}

func TestParseRetention(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 168 * time.Hour, "24h": 24 * time.Hour, "720h": 720 * time.Hour} {
		if got, err := ParseRetention(raw); err != nil || got != want {
			t.Errorf("%q gave %s %v", raw, got, err)
		}
	}
	for _, raw := range []string{"721h", "0s", "-1h", "x"} {
		if _, err := ParseRetention(raw); err == nil {
			t.Errorf("%q must fail", raw)
		}
	}
}

func TestStartAndStopSetRunning(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	deps := actionDeps(newFakeKubeVirt(t))
	for _, step := range []struct {
		action  func(context.Context, Deps, client.Client, client.ObjectKey) (Result, error)
		running bool
	}{{Stop, false}, {Stop, false}, {Start, true}, {Start, true}} {
		result, err := step.action(context.Background(), deps, c, key)
		if err != nil || result.Code != http.StatusAccepted {
			t.Fatalf("%+v %v", result, err)
		}
		if got := getVM(t, c, key.Namespace, key.Name).Spec.Running; got != step.running {
			t.Fatalf("running %v, want %v", got, step.running)
		}
	}
}

func TestRestartCallsKubeVirt(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	fake := newFakeKubeVirt(t)
	result, err := Restart(context.Background(), actionDeps(fake), c, key)
	if err != nil || result.Code != http.StatusAccepted {
		t.Fatalf("%+v %v", result, err)
	}
	seen := fake.seen()
	want := "/apis/subresources.kubevirt.io/v1/namespaces/" + key.Namespace + "/virtualmachines/web-1/restart"
	if len(seen) != 1 || seen[0].Method != http.MethodPut || seen[0].Path != want || seen[0].Authorization != "Bearer proxy-token" {
		t.Fatalf("requests %+v", seen)
	}
}

func TestActionNeedsRunningVM(t *testing.T) {
	c, _ := startEnv(t)
	fake := newFakeKubeVirt(t)
	for _, phase := range []string{"Stopped", "Starting", "Failed"} {
		key := actionVM(t, c, phase)
		for _, action := range []func(context.Context, Deps, client.Client, client.ObjectKey) (Result, error){Restart, Migrate} {
			_, err := action(context.Background(), actionDeps(fake), c, key)
			requireActionError(t, err, http.StatusConflict)
		}
	}
	if len(fake.seen()) != 0 {
		t.Fatalf("requests %+v", fake.seen())
	}
}

func TestMigrateCreatesMigration(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	result, err := Migrate(context.Background(), actionDeps(newFakeKubeVirt(t)), c, key)
	if err != nil || result.Code != http.StatusAccepted {
		t.Fatalf("%+v %v", result, err)
	}
	name := result.Body.(map[string]string)["migration"]
	migration := requirePresent(t, c, kubeVirtMigrationGVK, key.Namespace, name)
	vmi, _, _ := unstructured.NestedString(migration.Object, "spec", "vmiName")
	refs := migration.GetOwnerReferences()
	if vmi != "web-1" || len(refs) != 1 || refs[0].Name != "web-1" || migration.GetLabels()[v1alpha1.LabelName] != "web-1" || migration.GetLabels()[v1alpha1.LabelProject] != "web" {
		t.Fatalf("migration %+v", migration.Object)
	}
}

func TestSnapshotCreateAndList(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	deps := actionDeps(newFakeKubeVirt(t))
	empty, err := ListSnapshots(context.Background(), deps, c, key)
	if err != nil || empty.Code != http.StatusOK || len(empty.Body.([]SnapshotInfo)) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
	result, err := TakeSnapshot(context.Background(), deps, c, key, 24*time.Hour)
	if err != nil || result.Code != http.StatusCreated {
		t.Fatalf("%+v %v", result, err)
	}
	body := result.Body.(map[string]string)
	if body["name"] != "web-1-20261008120000" || body["expiresAt"] != "2026-10-09T12:00:00Z" {
		t.Fatalf("body %+v", body)
	}
	snapshot := requirePresent(t, c, vmSnapshotGVK, key.Namespace, "web-1-20261008120000")
	source, _, _ := unstructured.NestedMap(snapshot.Object, "spec", "source")
	if source["apiGroup"] != "kubevirt.io" || source["kind"] != "VirtualMachine" || source["name"] != "web-1" || snapshot.GetAnnotations()[AnnotationExpiresAt] != "2026-10-09T12:00:00Z" || len(snapshot.GetOwnerReferences()) != 1 || snapshot.GetLabels()[v1alpha1.LabelName] != "web-1" {
		t.Fatalf("snapshot %+v", snapshot.Object)
	}
	snapshot.Object["status"] = map[string]any{"phase": "Succeeded", "readyToUse": true}
	must(t, c.Status().Update(context.Background(), snapshot))
	listed, err := ListSnapshots(context.Background(), deps, c, key)
	must(t, err)
	infos := listed.Body.([]SnapshotInfo)
	if len(infos) != 1 || infos[0].Name != "web-1-20261008120000" || infos[0].Phase != "Succeeded" || !infos[0].ReadyToUse || infos[0].ExpiresAt != "2026-10-09T12:00:00Z" {
		t.Fatalf("infos %+v", infos)
	}
	_, err = TakeSnapshot(context.Background(), deps, c, key, time.Hour)
	requireActionError(t, err, http.StatusConflict)
}

func TestActionsUseOnlyTheProxyCredentials(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	fake := newFakeKubeVirt(t)
	_, err := Restart(context.Background(), actionDeps(fake), c, key)
	must(t, err)
	for _, seen := range fake.seen() {
		if seen.Authorization != "Bearer proxy-token" || seen.Cookie != "" {
			t.Fatalf("request %+v", seen)
		}
	}
}

func TestActionClientErrorIsReported(t *testing.T) {
	_, cfg := startEnv(t)
	base, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	must(t, err)
	key := actionVM(t, base, "Running")
	failing := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewInternalError(errors.New("boom"))
		},
	})
	_, err = Stop(context.Background(), actionDeps(newFakeKubeVirt(t)), failing, key)
	requireActionError(t, err, http.StatusInternalServerError)
	if !getVM(t, base, key.Namespace, key.Name).Spec.Running {
		t.Fatal("nothing may change")
	}
}

func TestRestartUsesACopyOfTheConfig(t *testing.T) {
	c, _ := startEnv(t)
	key := actionVM(t, c, "Running")
	deps := actionDeps(newFakeKubeVirt(t))
	before := rest.CopyConfig(deps.Config)
	_, err := Restart(context.Background(), deps, c, key)
	must(t, err)
	if deps.Config.Host != before.Host || deps.Config.BearerToken != before.BearerToken || deps.Config.Transport != nil || deps.Config.WrapTransport != nil {
		t.Fatalf("the config changed: %+v", deps.Config)
	}
}

func TestActionRefusesBadNames(t *testing.T) {
	c, _ := startEnv(t)
	fake := newFakeKubeVirt(t)
	for _, key := range []client.ObjectKey{{Namespace: "Acme..web", Name: "web-1"}, {Namespace: "acme-web", Name: "../x"}} {
		_, err := Start(context.Background(), actionDeps(fake), c, key)
		requireActionError(t, err, http.StatusBadRequest)
		_, err = Restart(context.Background(), actionDeps(fake), c, key)
		requireActionError(t, err, http.StatusBadRequest)
	}
	if len(fake.seen()) != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestActionVMNotFound(t *testing.T) {
	c, _ := startEnv(t)
	ns := uniqueNamespace(t, c)
	_, err := Stop(context.Background(), actionDeps(newFakeKubeVirt(t)), c, client.ObjectKey{Namespace: ns, Name: "ghost"})
	requireActionError(t, err, http.StatusNotFound)
}

func TestNewDepsKeepsTheConfig(t *testing.T) {
	cfg := &rest.Config{Host: "https://example.test"}
	deps := NewDeps(cfg)
	if deps.Config != cfg || deps.Now == nil {
		t.Fatalf("%+v", deps)
	}
}

func TestActionErrorStatus(t *testing.T) {
	status := conflict("busy").Status()
	if status.Kind != "Status" || status.Code != http.StatusConflict || status.Reason != metav1.StatusReasonConflict || status.Message != "busy" || status.Status != metav1.StatusFailure {
		t.Fatalf("%+v", status)
	}
}
