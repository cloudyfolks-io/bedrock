package container

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var cnbContainers = []string{"git-clone", "analyzer", "detector", "restorer", "builder"}

func cnbPod(initStates map[string]corev1.ContainerState, mainState *corev1.ContainerState, phase corev1.PodPhase) corev1.Pod {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "shop-abc"}}
	for _, name := range cnbContainers {
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{Name: name})
		if state, found := initStates[name]; found {
			pod.Status.InitContainerStatuses = append(pod.Status.InitContainerStatuses, corev1.ContainerStatus{Name: name, State: state})
		}
	}
	pod.Spec.Containers = []corev1.Container{{Name: "exporter"}}
	if mainState != nil {
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "exporter", State: *mainState}}
	}
	pod.Status.Phase = phase
	return pod
}

func exited(code int32) corev1.ContainerState {
	return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code}}
}

func runningState() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
}

func waitingState() corev1.ContainerState {
	return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}
}

func TestActiveContainer(t *testing.T) {
	allDone := map[string]corev1.ContainerState{}
	for _, name := range cnbContainers {
		allDone[name] = exited(0)
	}
	cases := []struct {
		name string
		pod  corev1.Pod
		want string
	}{
		{"no status yet", cnbPod(nil, nil, corev1.PodPending), "git-clone"},
		{"first init waiting", cnbPod(map[string]corev1.ContainerState{"git-clone": waitingState()}, nil, corev1.PodPending), "git-clone"},
		{"first init running", cnbPod(map[string]corev1.ContainerState{"git-clone": runningState()}, nil, corev1.PodPending), "git-clone"},
		{"first init done, second has no status", cnbPod(map[string]corev1.ContainerState{"git-clone": exited(0)}, nil, corev1.PodPending), "analyzer"},
		{"third init running", cnbPod(map[string]corev1.ContainerState{"git-clone": exited(0), "analyzer": exited(0), "detector": runningState()}, nil, corev1.PodPending), "detector"},
		{"an init that failed stays active", cnbPod(map[string]corev1.ContainerState{"git-clone": exited(0), "analyzer": exited(1)}, nil, corev1.PodFailed), "analyzer"},
		{"all inits done, main not started", cnbPod(allDone, nil, corev1.PodPending), "exporter"},
		{"all inits done, main running", cnbPod(allDone, addr(runningState()), corev1.PodRunning), "exporter"},
		{"pod ended", cnbPod(allDone, addr(exited(0)), corev1.PodSucceeded), "exporter"},
		{"an init that exited 0 after one that did not", cnbPod(map[string]corev1.ContainerState{"git-clone": exited(2), "analyzer": exited(0)}, nil, corev1.PodFailed), "git-clone"},
		{"no init containers", corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "build"}}}}, "build"},
		{"no containers", corev1.Pod{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActiveContainer(tc.pod); got != tc.want {
				t.Fatalf("active %q, want %q", got, tc.want)
			}
		})
	}
}

func addr[T any](value T) *T { return &value }

func TestParseBuildLogQuery(t *testing.T) {
	good := map[string]BuildLogQuery{
		"":                           {Tail: 2000},
		"follow=true":                {Tail: 2000, Follow: true},
		"tail=1":                     {Tail: 1},
		"tail=10000&container=build": {Tail: 10000, Container: "build"},
		"follow=false&tail=50":       {Tail: 50},
	}
	for raw, want := range good {
		values, _ := parseQuery(raw)
		got, err := ParseBuildLogQuery(values)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"tail=0", "tail=10001", "tail=x", "follow=maybe", "container=Bad_Name", "container=a/b"} {
		values, _ := parseQuery(raw)
		_, err := ParseBuildLogQuery(values)
		var actionErr ActionError
		if !errors.As(err, &actionErr) || actionErr.Code != http.StatusBadRequest {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

func parseQuery(raw string) (map[string][]string, error) {
	values := map[string][]string{}
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' }) {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = append(values[key], value)
	}
	return values, nil
}

type fakeBuildSource struct {
	mu      sync.Mutex
	pods    []*corev1.Pod
	polls   int
	logs    map[string]string
	opened  []string
	follows []bool
	openErr error
}

func (f *fakeBuildSource) Pod(context.Context) (*corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := min(f.polls, len(f.pods)-1)
	f.polls++
	return f.pods[index], nil
}

func (f *fakeBuildSource) Open(_ context.Context, name string, follow bool) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.opened = append(f.opened, name)
	f.follows = append(f.follows, follow)
	return io.NopCloser(strings.NewReader(f.logs[name])), nil
}

func podState(inits map[string]corev1.ContainerState, main *corev1.ContainerState, phase corev1.PodPhase) *corev1.Pod {
	pod := cnbPod(inits, main, phase)
	return &pod
}

func doneInits(upTo int) map[string]corev1.ContainerState {
	states := map[string]corev1.ContainerState{}
	for _, name := range cnbContainers[:upTo] {
		states[name] = exited(0)
	}
	return states
}

func TestStreamBuildLogsHandsOffFromTheCloneToEachPhase(t *testing.T) {
	running := addr(runningState())
	sequence := []*corev1.Pod{
		podState(map[string]corev1.ContainerState{"git-clone": waitingState()}, nil, corev1.PodPending),
		podState(map[string]corev1.ContainerState{"git-clone": runningState()}, nil, corev1.PodPending),
		podState(doneInits(1), nil, corev1.PodPending),
		podState(withState(doneInits(1), "analyzer", runningState()), nil, corev1.PodPending),
		podState(doneInits(3), nil, corev1.PodPending),
		podState(doneInits(5), running, corev1.PodRunning),
		podState(doneInits(5), addr(exited(0)), corev1.PodSucceeded),
	}
	source := &fakeBuildSource{pods: sequence, logs: map[string]string{
		"git-clone": "cloned\n", "analyzer": "analyzed\n", "detector": "detected\n", "restorer": "restored\n", "builder": "built\n", "exporter": "exported\n",
	}}
	var out strings.Builder
	if err := StreamBuildLogs(context.Background(), &out, source, true, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := "==> git-clone <==\ncloned\n==> analyzer <==\nanalyzed\n==> detector <==\ndetected\n==> restorer <==\nrestored\n==> builder <==\nbuilt\n==> exporter <==\nexported\n"
	if out.String() != want {
		t.Fatalf("output\n%s\nwant\n%s", out.String(), want)
	}
	if !slices.Equal(source.opened, cnbContainers[:5:5]) && !slices.Equal(source.opened, append(slices.Clone(cnbContainers), "exporter")) {
		t.Fatalf("opened %v", source.opened)
	}
	for _, follow := range source.follows {
		if !follow {
			t.Fatal("a followed stream opens every container with follow")
		}
	}
}

func withState(states map[string]corev1.ContainerState, name string, state corev1.ContainerState) map[string]corev1.ContainerState {
	states[name] = state
	return states
}

func TestStreamBuildLogsStopsWhenTheCloneFails(t *testing.T) {
	failed := podState(map[string]corev1.ContainerState{"git-clone": exited(128)}, nil, corev1.PodFailed)
	source := &fakeBuildSource{pods: []*corev1.Pod{failed}, logs: map[string]string{"git-clone": "fatal: repository not found\n", "analyzer": "must not appear\n"}}
	var out strings.Builder
	if err := StreamBuildLogs(context.Background(), &out, source, true, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if out.String() != "==> git-clone <==\nfatal: repository not found\n" || !slices.Equal(source.opened, []string{"git-clone"}) {
		t.Fatalf("output %q opened %v", out.String(), source.opened)
	}
}

func TestStreamBuildLogsWithoutFollowReadsWhatExistsAndReturns(t *testing.T) {
	source := &fakeBuildSource{
		pods: []*corev1.Pod{podState(withState(doneInits(2), "detector", runningState()), nil, corev1.PodPending)},
		logs: map[string]string{"git-clone": "c\n", "analyzer": "a\n", "detector": "d\n"},
	}
	var out strings.Builder
	if err := StreamBuildLogs(context.Background(), &out, source, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	if out.String() != "==> git-clone <==\nc\n==> analyzer <==\na\n==> detector <==\nd\n" || source.polls != 1 {
		t.Fatalf("output %q polls %d", out.String(), source.polls)
	}
	for _, follow := range source.follows {
		if follow {
			t.Fatal("no follow was asked")
		}
	}
}

func TestStreamBuildLogsWithoutFollowSkipsAContainerThatHasNotStarted(t *testing.T) {
	source := &fakeBuildSource{pods: []*corev1.Pod{podState(map[string]corev1.ContainerState{"git-clone": waitingState()}, nil, corev1.PodPending)}}
	var out strings.Builder
	if err := StreamBuildLogs(context.Background(), &out, source, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || len(source.opened) != 0 {
		t.Fatalf("output %q opened %v", out.String(), source.opened)
	}
}

func TestStreamBuildLogsEndsWhenThePodIsGone(t *testing.T) {
	source := &fakeBuildSource{pods: []*corev1.Pod{
		podState(map[string]corev1.ContainerState{"git-clone": runningState()}, nil, corev1.PodPending),
		nil,
	}, logs: map[string]string{"git-clone": "c\n"}}
	var out strings.Builder
	if err := StreamBuildLogs(context.Background(), &out, source, true, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if out.String() != "==> git-clone <==\nc\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestStreamBuildLogsStopsWhenTheRequestEnds(t *testing.T) {
	waiting := podState(map[string]corev1.ContainerState{"git-clone": waitingState()}, nil, corev1.PodPending)
	source := &fakeBuildSource{pods: []*corev1.Pod{waiting}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- StreamBuildLogs(ctx, io.Discard, source, true, 5*time.Millisecond) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not stop with the request")
	}
}

func TestStreamBuildLogsReportsAnOpenFailure(t *testing.T) {
	source := &fakeBuildSource{
		pods:    []*corev1.Pod{podState(map[string]corev1.ContainerState{"git-clone": runningState()}, nil, corev1.PodPending)},
		openErr: errors.New("boom"),
	}
	if err := StreamBuildLogs(context.Background(), io.Discard, source, true, time.Millisecond); err == nil {
		t.Fatal("an open failure must surface")
	}
}

type fakeBuildAPI struct {
	Server *httptest.Server
	mu     sync.Mutex
	asked  []string
}

func newFakeBuildAPI(t *testing.T, logs map[string]string) *fakeBuildAPI {
	t.Helper()
	fake := &fakeBuildAPI{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		key := parts[len(parts)-2] + "/" + r.URL.Query().Get("container")
		fake.mu.Lock()
		fake.asked = append(fake.asked, key+"?tail="+r.URL.Query().Get("tailLines")+"&follow="+r.URL.Query().Get("follow"))
		fake.mu.Unlock()
		body, found := logs[key]
		if !found {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(fake.Server.Close)
	return fake
}

func (f *fakeBuildAPI) deps() Deps {
	return Deps{Config: &rest.Config{Host: f.Server.URL, BearerToken: "proxy-token"}, Now: func() time.Time { return fixedNow }, LogPoll: 10 * time.Millisecond}
}

func buildPodIn(t *testing.T, c client.Client, namespace, name, build string) corev1.Pod {
	t.Helper()
	if err := c.Create(context.Background(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "default"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: BuildSelectorLabels(build)},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "git-clone", Image: "registry.test/git:1"}},
			Containers:     []corev1.Container{{Name: "build", Image: "registry.test/buildkit:1"}},
		},
	}
	createObject(t, c, &pod)
	return pod
}

func setPodProgress(t *testing.T, c client.Client, pod *corev1.Pod, init, main corev1.ContainerState, phase corev1.PodPhase) {
	t.Helper()
	updateStatus(t, c, pod, func(p *corev1.Pod) {
		p.Status.Phase = phase
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "git-clone", State: init}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "build", State: main}}
	})
}

func TestBuildLogsShowTheCloneThenTheBuild(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "shop", noBuildMutation)
	pod := buildPodIn(t, c, ns, "shop-abc", "shop")
	setPodProgress(t, c, &pod, exited(0), runningState(), corev1.PodRunning)
	api := newFakeBuildAPI(t, map[string]string{"shop-abc/git-clone": "cloned\n", "shop-abc/build": "built\n"})
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "shop"})
	code, body := get(t, server.URL+"/?tail=7")
	if code != http.StatusOK || body != "==> git-clone <==\ncloned\n==> build <==\nbuilt\n" {
		t.Fatalf("%d %q", code, body)
	}
	if !slices.Equal(api.asked, []string{"shop-abc/git-clone?tail=7&follow=", "shop-abc/build?tail=7&follow="}) {
		t.Fatalf("asked %v", api.asked)
	}
}

func TestBuildLogsOfOneContainerAreRaw(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "shop", noBuildMutation)
	pod := buildPodIn(t, c, ns, "shop-abc", "shop")
	setPodProgress(t, c, &pod, exited(0), runningState(), corev1.PodRunning)
	api := newFakeBuildAPI(t, map[string]string{"shop-abc/git-clone": "cloned\n", "shop-abc/build": "built\n"})
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "shop"})
	code, body := get(t, server.URL+"/?container=git-clone")
	if code != http.StatusOK || body != "cloned\n" {
		t.Fatalf("%d %q", code, body)
	}
	if code, _ := get(t, server.URL+"/?container=stranger"); code != http.StatusNotFound {
		t.Fatalf("a container that is not in the pod: %d", code)
	}
}

func TestBuildLogsRefuseAContainerThatHasNotStarted(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "shop", noBuildMutation)
	pod := buildPodIn(t, c, ns, "shop-abc", "shop")
	setPodProgress(t, c, &pod, runningState(), waitingState(), corev1.PodPending)
	api := newFakeBuildAPI(t, map[string]string{"shop-abc/git-clone": "cloning\n"})
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "shop"})
	if code, _ := get(t, server.URL+"/?container=build"); code != http.StatusConflict {
		t.Fatalf("code %d", code)
	}
}

func TestBuildLogsAreGoneWhenTheBuildEndedAndThePodIsGone(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	build := createBuild(t, c, ns, "old", noBuildMutation)
	updateStatus(t, c, &build, func(b *v1alpha1.Build) { b.Status.Phase = v1alpha1.BuildPhaseSucceeded })
	api := newFakeBuildAPI(t, nil)
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "old"})
	for _, query := range []string{"/", "/?follow=true"} {
		code, body := get(t, server.URL+query)
		if code != http.StatusGone || !strings.Contains(body, `"reason":"LogsExpired"`) {
			t.Fatalf("%s: %d %s", query, code, body)
		}
	}
	if len(api.asked) != 0 {
		t.Fatalf("the API server must not be asked: %v", api.asked)
	}
}

func TestBuildLogsOfAPendingBuildWithoutAPod(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "waiting", noBuildMutation)
	api := newFakeBuildAPI(t, map[string]string{"waiting-abc/git-clone": "cloned\n", "waiting-abc/build": "built\n"})
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "waiting"})
	if code, _ := get(t, server.URL+"/"); code != http.StatusNotFound {
		t.Fatalf("without follow: %d", code)
	}

	done := make(chan struct{})
	var code int
	var body string
	go func() {
		defer close(done)
		code, body = get(t, server.URL+"/?follow=true")
	}()
	time.Sleep(100 * time.Millisecond)
	pod := buildPodIn(t, c, ns, "waiting-abc", "waiting")
	setPodProgress(t, c, &pod, exited(0), exited(0), corev1.PodSucceeded)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the follow request did not end")
	}
	if code != http.StatusOK || body != "==> git-clone <==\ncloned\n==> build <==\nbuilt\n" {
		t.Fatalf("%d %q", code, body)
	}
}

func TestBuildLogsRefuseBadInput(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	api := newFakeBuildAPI(t, nil)
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "ghost"})
	if code, _ := get(t, server.URL+"/"); code != http.StatusNotFound {
		t.Fatalf("a build that does not exist: %d", code)
	}
	if code, _ := get(t, server.URL+"/?tail=0"); code != http.StatusBadRequest {
		t.Fatalf("tail 0: %d", code)
	}
	response, err := http.Post(server.URL+"/", "text/plain", nil)
	must(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("post: %d %v", response.StatusCode, response.Header)
	}
	bad := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: "Bad_Namespace", Name: "x"})
	if code, _ := get(t, bad.URL+"/"); code != http.StatusBadRequest {
		t.Fatalf("a bad namespace: %d", code)
	}
}

func TestBuildLogsUseTheNewestPodAndIgnoreOtherBuilds(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "shop", noBuildMutation)
	createBuild(t, c, ns, "other", noBuildMutation)
	other := buildPodIn(t, c, ns, "other-abc", "other")
	setPodProgress(t, c, &other, exited(0), runningState(), corev1.PodRunning)
	time.Sleep(1100 * time.Millisecond)
	mine := buildPodIn(t, c, ns, "shop-new", "shop")
	setPodProgress(t, c, &mine, exited(0), runningState(), corev1.PodRunning)
	api := newFakeBuildAPI(t, map[string]string{"shop-new/git-clone": "mine\n", "shop-new/build": "mine too\n", "other-abc/git-clone": "secret\n", "other-abc/build": "secret\n"})
	server := serveHandler(t, BuildLogs(api.deps()), c, client.ObjectKey{Namespace: ns, Name: "shop"})
	code, body := get(t, server.URL+"/")
	if code != http.StatusOK || strings.Contains(body, "secret") || !strings.Contains(body, "mine too") {
		t.Fatalf("%d %q", code, body)
	}
}

func TestRegisterSubresourcesAddsBuildLogs(t *testing.T) {
	reg := newRegistry()
	if err := registerWith(reg, Deps{}); err != nil {
		t.Fatal(err)
	}
	if _, found := reg.Lookup("builds", "logs"); !found {
		t.Fatal("the subresource builds/logs is not registered")
	}
	if _, found := reg.Lookup("builds", "exec"); found {
		t.Fatal("a build has no exec")
	}
}
