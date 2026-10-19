package container

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	defaultBuildTail         = 2000
	defaultBuildLogPoll      = time.Second
	ReasonLogsExpired        = "LogsExpired"
	buildLogHeaderFormat     = "==> %s <==\n"
	podStartedWaitingMessage = "the build has no pod yet"
)

type BuildLogQuery struct {
	Follow    bool
	Tail      int64
	Container string
}

func ParseBuildLogQuery(values map[string][]string) (BuildLogQuery, error) {
	first := func(key string) string {
		if v := values[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	query := BuildLogQuery{Tail: defaultBuildTail, Container: first("container")}
	if query.Container != "" && len(validation.IsDNS1123Label(query.Container)) > 0 {
		return BuildLogQuery{}, badRequest("container must be a container name of the build pod")
	}
	if raw := first("tail"); raw != "" {
		tail, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || tail < minTail || tail > maxTail {
			return BuildLogQuery{}, badRequest("tail must be a number from %d to %d", minTail, maxTail)
		}
		query.Tail = tail
	}
	if raw := first("follow"); raw != "" {
		follow, err := strconv.ParseBool(raw)
		if err != nil {
			return BuildLogQuery{}, badRequest("follow must be true or false")
		}
		query.Follow = follow
	}
	return query, nil
}

func ActiveContainer(pod corev1.Pod) string {
	for _, init := range pod.Spec.InitContainers {
		status, found := containerStatus(pod.Status.InitContainerStatuses, init.Name)
		if !found || status.State.Terminated == nil || status.State.Terminated.ExitCode != 0 {
			return init.Name
		}
	}
	if len(pod.Spec.Containers) == 0 {
		return ""
	}
	return pod.Spec.Containers[0].Name
}

func containerStatus(statuses []corev1.ContainerStatus, name string) (corev1.ContainerStatus, bool) {
	for _, status := range statuses {
		if status.Name == name {
			return status, true
		}
	}
	return corev1.ContainerStatus{}, false
}

func containersInOrder(pod corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, init := range pod.Spec.InitContainers {
		names = append(names, init.Name)
	}
	for _, main := range pod.Spec.Containers {
		names = append(names, main.Name)
	}
	return names
}

func containersUpToActive(pod corev1.Pod) []string {
	names := containersInOrder(pod)
	active := ActiveContainer(pod)
	index := slices.Index(names, active)
	if index < 0 {
		return nil
	}
	return names[:index+1]
}

func containerStarted(pod corev1.Pod, name string) bool {
	status, found := containerStatus(pod.Status.InitContainerStatuses, name)
	if !found {
		status, found = containerStatus(pod.Status.ContainerStatuses, name)
	}
	return found && (status.State.Running != nil || status.State.Terminated != nil)
}

func podEnded(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

type BuildLogSource interface {
	Pod(ctx context.Context) (*corev1.Pod, error)
	Open(ctx context.Context, container string, follow bool) (io.ReadCloser, error)
}

func StreamBuildLogs(ctx context.Context, out io.Writer, source BuildLogSource, follow bool, poll time.Duration) error {
	streamed := map[string]bool{}
	for {
		pod, err := source.Pod(ctx)
		if err != nil {
			return err
		}
		if pod == nil {
			return nil
		}
		ended := podEnded(*pod)
		for _, name := range containersUpToActive(*pod) {
			if streamed[name] || !containerStarted(*pod, name) {
				continue
			}
			if err := copyContainerLog(ctx, out, source, name, follow); err != nil {
				return err
			}
			streamed[name] = true
		}
		if !follow || ended {
			return nil
		}
		if err := sleepOrDone(ctx, poll); err != nil {
			return nil
		}
	}
}

func copyContainerLog(ctx context.Context, out io.Writer, source BuildLogSource, name string, follow bool) error {
	stream, err := source.Open(ctx, name, follow)
	if err != nil {
		return err
	}
	defer stream.Close()
	if _, err := fmt.Fprintf(out, buildLogHeaderFormat, name); err != nil {
		return err
	}
	_, err = io.Copy(out, stream)
	return err
}

func sleepOrDone(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type clusterBuildLogs struct {
	reader    client.Client
	clientset kubernetes.Interface
	key       client.ObjectKey
	podName   string
	tail      int64
}

func (s clusterBuildLogs) Pod(ctx context.Context) (*corev1.Pod, error) {
	var pod corev1.Pod
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.key.Namespace, Name: s.podName}, &pod)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError(err)
	}
	if !pod.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	return &pod, nil
}

func (s clusterBuildLogs) Open(ctx context.Context, name string, follow bool) (io.ReadCloser, error) {
	options := &corev1.PodLogOptions{Container: name, Follow: follow, TailLines: &s.tail}
	stream, err := s.clientset.CoreV1().Pods(s.key.Namespace).GetLogs(s.podName, options).Stream(ctx)
	if err != nil {
		return nil, logFailure(err)
	}
	return stream, nil
}

func loadBuild(ctx context.Context, c client.Client, key client.ObjectKey) (v1alpha1.Build, error) {
	var build v1alpha1.Build
	if len(validation.IsDNS1123Label(key.Namespace)) > 0 || len(validation.IsDNS1123Label(key.Name)) > 0 {
		return build, badRequest("the namespace and the name must be DNS labels")
	}
	err := c.Get(ctx, key, &build)
	if apierrors.IsNotFound(err) {
		return build, notFoundError("the build %q does not exist", key.Name)
	}
	if err != nil {
		return build, internalError(err)
	}
	return build, nil
}

func newestBuildPod(ctx context.Context, c client.Client, key client.ObjectKey) (*corev1.Pod, error) {
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(key.Namespace), client.MatchingLabels(BuildSelectorLabels(key.Name))); err != nil {
		return nil, internalError(err)
	}
	pods := slices.DeleteFunc(list.Items, func(pod corev1.Pod) bool { return !pod.DeletionTimestamp.IsZero() })
	if len(pods) == 0 {
		return nil, nil
	}
	newest := slices.MaxFunc(pods, func(a, b corev1.Pod) int {
		if byTime := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); byTime != 0 {
			return byTime
		}
		return strings.Compare(a.Name, b.Name)
	})
	return &newest, nil
}

func (d Deps) logPoll() time.Duration {
	if d.LogPoll > 0 {
		return d.LogPoll
	}
	return defaultBuildLogPoll
}

func gone(reason metav1.StatusReason, format string, args ...any) ActionError {
	return ActionError{Code: http.StatusGone, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func BuildLogs(d Deps) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if err := serveBuildLogs(ctx, d, c, key, w, r); err != nil {
			writeError(w, err)
		}
	}
}

func serveBuildLogs(ctx context.Context, d Deps, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) error {
	query, err := ParseBuildLogQuery(r.URL.Query())
	if err != nil {
		return err
	}
	pod, err := awaitBuildPod(ctx, d, c, key, query.Follow)
	if err != nil {
		return err
	}
	if query.Container != "" && !slices.Contains(containersInOrder(*pod), query.Container) {
		return notFoundError("the container %q does not belong to the build pod", query.Container)
	}
	if query.Container != "" && !containerStarted(*pod, query.Container) {
		return conflict("the container %q has not started", query.Container)
	}
	clientset, err := kubernetes.NewForConfig(d.Config)
	if err != nil {
		return internalError(err)
	}
	source := clusterBuildLogs{reader: c, clientset: clientset, key: key, podName: pod.Name, tail: query.Tail}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	out := flushWriter{w: w, c: http.NewResponseController(w)}
	if query.Container != "" {
		_ = copySingleContainer(ctx, out, source, query.Container, query.Follow)
		return nil
	}
	_ = StreamBuildLogs(ctx, out, source, query.Follow, d.logPoll())
	return nil
}

func copySingleContainer(ctx context.Context, out io.Writer, source BuildLogSource, name string, follow bool) error {
	stream, err := source.Open(ctx, name, follow)
	if err != nil {
		return err
	}
	defer stream.Close()
	_, err = io.Copy(out, stream)
	return err
}

func awaitBuildPod(ctx context.Context, d Deps, c client.Client, key client.ObjectKey, follow bool) (*corev1.Pod, error) {
	for {
		build, err := loadBuild(ctx, c, key)
		if err != nil {
			return nil, err
		}
		pod, err := newestBuildPod(ctx, c, key)
		if err != nil {
			return nil, err
		}
		if pod != nil {
			return pod, nil
		}
		if IsTerminalBuildPhase(build.Status.Phase) {
			return nil, gone(ReasonLogsExpired, "the pod of the build is gone: the logs are no longer available")
		}
		if !follow {
			return nil, notFoundError(podStartedWaitingMessage)
		}
		if err := sleepOrDone(ctx, d.logPoll()); err != nil {
			return nil, notFoundError(podStartedWaitingMessage)
		}
	}
}
