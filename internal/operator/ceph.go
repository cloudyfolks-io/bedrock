package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	rookOperatorContainer = "rook-ceph-operator"
	cephHealthOK          = "HEALTH_OK"
	cephActiveClean       = "active+clean"
)

type execFunc func(ctx context.Context, namespace, pod, container string, command []string) ([]byte, error)

type cephReport struct {
	Skipped  bool
	Health   string
	PGs      int64
	CleanPGs int64
}

func cephCommand() []string {
	return []string{"ceph", "status", "--format", "json", "--connect-timeout=10", "--conf=/var/lib/rook/" + storageNamespace + "/" + storageNamespace + ".config"}
}

func readCeph(ctx context.Context, c client.Client, exec execFunc) (cephReport, error) {
	var clusters unstructured.UnstructuredList
	clusters.SetGroupVersionKind(cephClusterGVK.GroupVersion().WithKind(cephClusterGVK.Kind + "List"))
	err := c.List(ctx, &clusters, client.InNamespace(storageNamespace))
	if meta.IsNoMatchError(err) || (err == nil && len(clusters.Items) == 0) {
		return cephReport{Skipped: true}, nil
	}
	if err != nil {
		return cephReport{}, err
	}
	pod, err := rookOperatorPod(ctx, c)
	if err != nil {
		return cephReport{}, err
	}
	out, err := exec(ctx, storageNamespace, pod, rookOperatorContainer, cephCommand())
	if err != nil {
		return cephReport{}, err
	}
	return parseCephStatus(out)
}

func rookOperatorPod(ctx context.Context, c client.Client) (string, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(storageNamespace), client.MatchingLabels{"app": "rook-ceph-operator"}); err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running rook-ceph-operator pod in %s", storageNamespace)
}

func parseCephStatus(data []byte) (cephReport, error) {
	var status struct {
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
		PGMap struct {
			NumPGs  int64 `json:"num_pgs"`
			ByState []struct {
				Name  string `json:"state_name"`
				Count int64  `json:"count"`
			} `json:"pgs_by_state"`
		} `json:"pgmap"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return cephReport{}, fmt.Errorf("parse ceph status: %w", err)
	}
	if status.Health.Status == "" {
		return cephReport{}, errors.New("ceph status has no health")
	}
	var clean int64
	for _, state := range status.PGMap.ByState {
		if state.Name == cephActiveClean {
			clean += state.Count
		}
	}
	return cephReport{Health: status.Health.Status, PGs: status.PGMap.NumPGs, CleanPGs: clean}, nil
}

func cephPGProblem(report cephReport) string {
	if report.Skipped || report.CleanPGs == report.PGs {
		return ""
	}
	return fmt.Sprintf("ceph: %d of %d PGs are not %s", report.PGs-report.CleanPGs, report.PGs, cephActiveClean)
}

func cephHealthProblem(report cephReport) string {
	if report.Skipped {
		return ""
	}
	if report.Health != cephHealthOK {
		return fmt.Sprintf("ceph: health is %s", report.Health)
	}
	return cephPGProblem(report)
}

func podExec(config *rest.Config) (execFunc, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, namespace, pod, container string, command []string) ([]byte, error) {
		request := clientset.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
		executor, err := remotecommand.NewWebSocketExecutor(config, "GET", request.URL().String())
		if err != nil {
			return nil, err
		}
		var stdout, stderr bytes.Buffer
		if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				err = fmt.Errorf("%w: %s", err, detail)
			}
			return nil, fmt.Errorf("exec %s in %s/%s: %w", strings.Join(command, " "), namespace, pod, err)
		}
		return stdout.Bytes(), nil
	}, nil
}
