package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clientexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type Kind string

const (
	KindUnclassified Kind = ""
	KindExists       Kind = "Exists"
	KindNotFound     Kind = "NotFound"
	KindOwnsObjects  Kind = "OwnsObjects"
)

var (
	ErrNotReady       = errors.New("the database has no ready pod yet")
	ErrUnavailable    = errors.New("the database command could not run")
	ErrOutputTooLarge = errors.New("the database command returned too much output")
	ErrBusy           = errors.New("another command is running on this database")
)

type CommandError struct {
	Kind     Kind
	ExitCode int
}

func (e *CommandError) Error() string {
	if e.Kind == KindUnclassified {
		return fmt.Sprintf("the database rejected the command (exit code %d)", e.ExitCode)
	}
	return fmt.Sprintf("the database rejected the command: %s", e.Kind)
}

func IsKind(err error, kind Kind) bool {
	var command *CommandError
	return errors.As(err, &command) && command.Kind == kind
}

type Target struct {
	Namespace string
	Selector  client.MatchingLabels
	Container string
	Command   []string
	Classify  func(stderr string) Kind
}

type Request struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	Stdin     []byte
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Capped   bool
}

type Executor interface {
	Exec(ctx context.Context, request Request) (Result, error)
}

type Locks struct{ held sync.Map }

func (l *Locks) Acquire(ctx context.Context, key string) (func(), error) {
	value, _ := l.held.LoadOrStore(key, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ErrBusy
	}
}

type Runner struct {
	Pods     client.Reader
	Executor Executor
	Locks    *Locks
	Timeout  time.Duration
}

func (r Runner) Run(ctx context.Context, key string, target Target, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	release, err := r.Locks.Acquire(ctx, key)
	if err != nil {
		return "", err
	}
	defer release()
	pod, err := r.readyPod(ctx, target)
	if err != nil {
		return "", err
	}
	result, err := r.Executor.Exec(ctx, Request{Namespace: target.Namespace, Pod: pod, Container: target.Container, Command: target.Command, Stdin: []byte(stdin)})
	if err != nil {
		return "", ErrUnavailable
	}
	if result.Capped {
		return "", ErrOutputTooLarge
	}
	if result.ExitCode != 0 {
		return "", r.failure(ctx, key, target, result)
	}
	return string(result.Stdout), nil
}

func (r Runner) failure(ctx context.Context, key string, target Target, result Result) error {
	kind := KindUnclassified
	if target.Classify != nil {
		kind = target.Classify(string(result.Stderr))
	}
	logf.FromContext(ctx).Info("engine command failed", "database", key, "exitCode", result.ExitCode, "reason", string(kind))
	return &CommandError{Kind: kind, ExitCode: result.ExitCode}
}

func (r Runner) readyPod(ctx context.Context, target Target) (string, error) {
	var pods corev1.PodList
	if err := r.Pods.List(ctx, &pods, client.InNamespace(target.Namespace), target.Selector); err != nil {
		return "", ErrUnavailable
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && podReady(pod) {
			return pod.Name, nil
		}
	}
	return "", ErrNotReady
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

type KubeExecutor struct {
	Config      *rest.Config
	OutputLimit int
}

func (e KubeExecutor) Exec(ctx context.Context, request Request) (Result, error) {
	clientset, err := kubernetes.NewForConfig(e.Config)
	if err != nil {
		return Result{}, err
	}
	url := clientset.CoreV1().RESTClient().Post().Resource("pods").Namespace(request.Namespace).Name(request.Pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: request.Container, Command: request.Command, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewWebSocketExecutor(e.Config, http.MethodGet, url.String())
	if err != nil {
		return Result{}, err
	}
	stdout, stderr := &limitedBuffer{limit: e.OutputLimit}, &limitedBuffer{limit: e.OutputLimit}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(request.Stdin), Stdout: stdout, Stderr: stderr})
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Capped: stdout.capped || stderr.capped}
	var exit clientexec.CodeExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.Code
		return result, nil
	}
	return result, err
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
	capped bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	room := max(b.limit-b.buffer.Len(), 0)
	if len(p) > room {
		b.capped = true
		p = p[:room]
	}
	b.buffer.Write(p)
	return written, nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }
