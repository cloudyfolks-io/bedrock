package container

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultTail  = 200
	minTail      = 1
	maxTail      = 10000
	logContainer = appContainerName
)

type LogQuery struct {
	Pod        string
	Tail       int64
	Follow     bool
	Previous   bool
	Timestamps bool
}

func ParseLogQuery(values map[string][]string) (LogQuery, error) {
	first := func(key string) string {
		if v := values[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	query := LogQuery{Pod: first("pod"), Tail: defaultTail}
	if raw := first("tail"); raw != "" {
		tail, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || tail < minTail || tail > maxTail {
			return LogQuery{}, badRequest("tail must be a number from %d to %d", minTail, maxTail)
		}
		query.Tail = tail
	}
	flags := []struct {
		key  string
		into *bool
	}{{"follow", &query.Follow}, {"previous", &query.Previous}, {"timestamps", &query.Timestamps}}
	for _, flag := range flags {
		if raw := first(flag.key); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return LogQuery{}, badRequest("%s must be true or false", flag.key)
			}
			*flag.into = parsed
		}
	}
	return query, nil
}

func (q LogQuery) options() *corev1.PodLogOptions {
	return &corev1.PodLogOptions{Container: logContainer, Follow: q.Follow, Previous: q.Previous, Timestamps: q.Timestamps, TailLines: &q.Tail}
}

type NamedStream struct {
	Pod    string
	Stream io.Reader
}

func MergeLogs(out io.Writer, streams []NamedStream) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var first error
	write := func(prefix string, line []byte) {
		mu.Lock()
		defer mu.Unlock()
		if _, err := fmt.Fprintf(out, "%s%s", prefix, line); err != nil && first == nil {
			first = err
		}
	}
	for _, source := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := bufio.NewReaderSize(source.Stream, 64<<10)
			prefix := "[" + source.Pod + "] "
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					if line[len(line)-1] != '\n' {
						line = append(line, '\n')
					}
					write(prefix, line)
				}
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}

type flushWriter struct {
	w http.ResponseWriter
	c *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		_ = f.c.Flush()
	}
	return n, err
}

func Logs(d Deps) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if err := serveLogs(ctx, d, c, key, w, r); err != nil {
			writeError(w, err)
		}
	}
}

func serveLogs(ctx context.Context, d Deps, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) error {
	query, err := ParseLogQuery(r.URL.Query())
	if err != nil {
		return err
	}
	if _, err := loadContainer(ctx, c, key); err != nil {
		return err
	}
	all, err := podsOf(ctx, c, key)
	if err != nil {
		return err
	}
	pods, err := PickPods(all, query.Pod)
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(d.Config)
	if err != nil {
		return internalError(err)
	}
	streams := make([]NamedStream, 0, len(pods))
	for _, pod := range pods {
		stream, err := clientset.CoreV1().Pods(key.Namespace).GetLogs(pod.Name, query.options()).Stream(ctx)
		if err != nil {
			closeAll(streams)
			return logFailure(err)
		}
		streams = append(streams, NamedStream{Pod: pod.Name, Stream: stream})
	}
	defer closeAll(streams)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	out := flushWriter{w: w, c: http.NewResponseController(w)}
	if len(streams) == 1 {
		_, _ = io.Copy(out, streams[0].Stream)
		return nil
	}
	_ = MergeLogs(out, streams)
	return nil
}

func logFailure(err error) error {
	if apierrors.IsBadRequest(err) {
		return badRequest("%s", err.Error())
	}
	return unreachable(err)
}

func closeAll(streams []NamedStream) {
	for _, stream := range streams {
		if closer, ok := stream.Stream.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}
