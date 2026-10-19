package exec

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type fakeExecutor struct {
	mu       sync.Mutex
	requests []Request
	result   Result
	err      error
	block    chan struct{}
}

func (f *fakeExecutor) Exec(ctx context.Context, request Request) (Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return f.result, f.err
}

func pod(name string, ready bool, phase corev1.PodPhase, deleting bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bedrock-dbaas", Labels: map[string]string{"cnpg.io/cluster": "db1", "cnpg.io/instanceRole": "primary"}},
		Status:     corev1.PodStatus{Phase: phase},
	}
	if ready {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	if deleting {
		now := metav1.Now()
		p.DeletionTimestamp = &now
		p.Finalizers = []string{"test"}
	}
	return p
}

func target() Target {
	return Target{
		Namespace: "bedrock-dbaas",
		Selector:  client.MatchingLabels{"cnpg.io/cluster": "db1", "cnpg.io/instanceRole": "primary"},
		Container: "postgres",
		Command:   []string{"psql"},
		Classify: func(stderr string) Kind {
			if strings.Contains(stderr, "already") {
				return KindExists
			}
			return KindUnclassified
		},
	}
}

func runner(exec Executor, pods ...client.Object) Runner {
	return Runner{Pods: fake.NewClientBuilder().WithObjects(pods...).Build(), Executor: exec, Locks: &Locks{}, Timeout: 2 * time.Second}
}

func TestRunSendsTheScriptOnStdinToTheReadyPod(t *testing.T) {
	exec := &fakeExecutor{result: Result{Stdout: []byte("ok\n")}}
	r := runner(exec, pod("a-notready", false, corev1.PodRunning, false), pod("b-pending", true, corev1.PodPending, false), pod("c-deleting", true, corev1.PodRunning, true), pod("d-good", true, corev1.PodRunning, false))
	out, err := r.Run(context.Background(), "db1", target(), "SELECT 1;")
	if err != nil || out != "ok\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	request := exec.requests[0]
	if request.Pod != "d-good" || request.Container != "postgres" || request.Namespace != "bedrock-dbaas" || string(request.Stdin) != "SELECT 1;" || strings.Join(request.Command, " ") != "psql" {
		t.Fatalf("request %+v", request)
	}
}

func TestRunWithoutAReadyPodIsNotReady(t *testing.T) {
	r := runner(&fakeExecutor{}, pod("a", false, corev1.PodRunning, false))
	if _, err := r.Run(context.Background(), "db1", target(), "x"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("err %v", err)
	}
}

func TestRunMapsExecutorFailureOutputLimitAndExitCode(t *testing.T) {
	pods := pod("a", true, corev1.PodRunning, false)
	if _, err := runner(&fakeExecutor{err: errors.New("boom")}, pods).Run(context.Background(), "db1", target(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("executor error: %v", err)
	}
	if _, err := runner(&fakeExecutor{result: Result{Capped: true}}, pods).Run(context.Background(), "db1", target(), "x"); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("capped: %v", err)
	}
	_, err := runner(&fakeExecutor{result: Result{ExitCode: 3, Stderr: []byte("ERROR: role already exists")}}, pods).Run(context.Background(), "db1", target(), "x")
	var command *CommandError
	if !errors.As(err, &command) || command.Kind != KindExists || command.ExitCode != 3 || !IsKind(err, KindExists) || IsKind(err, KindNotFound) {
		t.Fatalf("classified: %v", err)
	}
	_, err = runner(&fakeExecutor{result: Result{ExitCode: 1, Stderr: []byte("something else")}}, pods).Run(context.Background(), "db1", target(), "x")
	if !errors.As(err, &command) || command.Kind != KindUnclassified {
		t.Fatalf("unclassified: %v", err)
	}
}

func TestRunTimesOutAndSerializesCommandsOfOneDatabase(t *testing.T) {
	pods := pod("a", true, corev1.PodRunning, false)
	blocked := &fakeExecutor{block: make(chan struct{})}
	r := runner(blocked, pods)
	r.Timeout = 200 * time.Millisecond
	if _, err := r.Run(context.Background(), "db1", target(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout: %v", err)
	}

	hold := &fakeExecutor{block: make(chan struct{})}
	first := runner(hold, pods)
	first.Timeout = 5 * time.Second
	done := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background(), "db2", target(), "x")
		done <- err
	}()
	for len(func() []Request { hold.mu.Lock(); defer hold.mu.Unlock(); return hold.requests }()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	second := Runner{Pods: first.Pods, Executor: &fakeExecutor{}, Locks: first.Locks, Timeout: 150 * time.Millisecond}
	if _, err := second.Run(context.Background(), "db2", target(), "y"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second command: %v", err)
	}
	if _, err := second.Run(context.Background(), "db3", target(), "y"); err != nil {
		t.Fatalf("another database must not wait: %v", err)
	}
	close(hold.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunNeverLogsTheScriptTheCommandOrTheOutput(t *testing.T) {
	var lines []string
	sink := funcr.New(func(prefix, args string) { lines = append(lines, prefix+" "+args) }, funcr.Options{Verbosity: 5})
	ctx := logf.IntoContext(context.Background(), sink)
	exec := &fakeExecutor{result: Result{ExitCode: 1, Stdout: []byte("STDOUT-SECRET"), Stderr: []byte("ERROR: bad PASSWORD-IN-STDERR")}}
	tgt := target()
	tgt.Command = []string{"psql", "COMMAND-MARKER"}
	_, _ = runner(exec, pod("a", true, corev1.PodRunning, false)).Run(ctx, "db1", tgt, "ALTER ROLE x PASSWORD 'SCRIPT-MARKER';")
	if len(lines) == 0 {
		t.Fatal("a failed command must leave one log line")
	}
	for _, line := range lines {
		for _, marker := range []string{"SCRIPT-MARKER", "COMMAND-MARKER", "STDOUT-SECRET", "PASSWORD-IN-STDERR"} {
			if strings.Contains(line, marker) {
				t.Fatalf("log line %q holds %s", line, marker)
			}
		}
	}
}

func TestLimitedBufferStopsAtTheLimitAndKeepsTheStreamOpen(t *testing.T) {
	buffer := &limitedBuffer{limit: 5}
	for _, chunk := range []string{"abc", "defgh", "ij"} {
		n, err := buffer.Write([]byte(chunk))
		if n != len(chunk) || err != nil {
			t.Fatalf("write %q returned %d %v", chunk, n, err)
		}
	}
	if string(buffer.Bytes()) != "abcde" || !buffer.capped {
		t.Fatalf("buffer %q capped %v", buffer.Bytes(), buffer.capped)
	}
}
