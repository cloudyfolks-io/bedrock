package database

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

const (
	subresourceKind = "databases"
	DefaultLogTail  = 200
	MaxLogTail      = 500
	MaxLogBytes     = 1 << 20
)

type LogSource func(ctx context.Context, namespace, pod, container string, tail int64) (io.ReadCloser, error)

func NewLogSource(cfg *rest.Config) (LogSource, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, namespace, pod, container string, tail int64) (io.ReadCloser, error) {
		options := &corev1.PodLogOptions{Container: container, TailLines: &tail}
		return clientset.CoreV1().Pods(namespace).GetLogs(pod, options).Stream(ctx)
	}, nil
}

func ProxyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"databases"}, Verbs: []string{"get"}}}
}

func RegisterSubresources(reg *proxy.Subresources) error {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	source, err := NewLogSource(cfg)
	if err != nil {
		return err
	}
	return registerWith(reg, source)
}

func registerWith(reg *proxy.Subresources, source LogSource) error {
	return reg.Register(subresourceKind, "logs", proxy.SubresourceHandler(Logs(source)))
}

func ParseTail(raw string) (int64, error) {
	if raw == "" {
		return DefaultLogTail, nil
	}
	tail, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	switch {
	case tail < 1:
		return DefaultLogTail, nil
	case tail > MaxLogTail:
		return MaxLogTail, nil
	}
	return tail, nil
}

func Logs(source LogSource) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "the method is not allowed")
			return
		}
		tail, err := ParseTail(r.URL.Query().Get("tail"))
		if err != nil {
			writeStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, "tail must be a number")
			return
		}
		serveLogs(ctx, source, c, key, tail, w)
	}
}

func serveLogs(ctx context.Context, source LogSource, c client.Client, key client.ObjectKey, tail int64, w http.ResponseWriter) {
	var db v1alpha1.Database
	if err := c.Get(ctx, key, &db); err != nil {
		if apierrors.IsNotFound(err) {
			writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "the database does not exist")
			return
		}
		failed(w, err)
		return
	}
	target := TargetFor(db.Spec.Engine, Name(db.UID))
	pod, found, err := readyPod(ctx, c, target.Selector)
	if err != nil {
		failed(w, err)
		return
	}
	if !found {
		writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict, "the database has no ready pod")
		return
	}
	stream, err := source(ctx, PlatformNamespace, pod.Name, target.Container, tail)
	if err != nil {
		logf.Log.WithName("database").Error(err, "the logs are not reachable")
		writeStatus(w, http.StatusBadGateway, metav1.StatusReasonServiceUnavailable, "the logs of the database are not reachable")
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(stream, MaxLogBytes))
}

func readyPod(ctx context.Context, c client.Client, selector string) (corev1.Pod, bool, error) {
	parsed, err := labels.Parse(selector)
	if err != nil {
		return corev1.Pod{}, false, err
	}
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(PlatformNamespace), client.MatchingLabelsSelector{Selector: parsed}); err != nil {
		return corev1.Pod{}, false, err
	}
	pods := list.Items
	slices.SortFunc(pods, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	for _, pod := range pods {
		if network.PodReady(pod) {
			return pod, true, nil
		}
	}
	return corev1.Pod{}, false, nil
}

func failed(w http.ResponseWriter, err error) {
	logf.Log.WithName("database").Error(err, "the request failed")
	writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "the request failed; ask an administrator to read the operator log")
}
