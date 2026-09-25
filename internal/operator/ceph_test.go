package operator

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const cephStatusJSON = `{
  "fsid": "1f0c1e44-7f1e-4b7a-9f53-5d1c0d5c8b21",
  "health": {"status": "HEALTH_OK", "checks": {}, "mutes": []},
  "pgmap": {
    "pgs_by_state": [{"state_name": "active+clean", "count": 30}, {"state_name": "active+undersized+degraded", "count": 3}],
    "num_pgs": 33,
    "num_pools": 3
  }
}`

func TestParseCephStatus(t *testing.T) {
	got, err := parseCephStatus([]byte(cephStatusJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got != (cephReport{Health: "HEALTH_OK", PGs: 33, CleanPGs: 30}) {
		t.Fatalf("report %+v", got)
	}
	if _, err := parseCephStatus([]byte(`{"pgmap": {}}`)); err == nil || err.Error() != "ceph status has no health" {
		t.Fatalf("missing health: %v", err)
	}
	if _, err := parseCephStatus([]byte(`not json`)); err == nil || !strings.HasPrefix(err.Error(), "parse ceph status: ") {
		t.Fatalf("bad json: %v", err)
	}
}

func TestCephProblems(t *testing.T) {
	cases := map[string]struct {
		report cephReport
		health string
		pgs    string
	}{
		"skipped":            {cephReport{Skipped: true}, "", ""},
		"healthy":            {cephReport{Health: "HEALTH_OK", PGs: 33, CleanPGs: 33}, "", ""},
		"no pools":           {cephReport{Health: "HEALTH_OK"}, "", ""},
		"warn and clean":     {cephReport{Health: "HEALTH_WARN", PGs: 33, CleanPGs: 33}, "ceph: health is HEALTH_WARN", ""},
		"ok and not clean":   {cephReport{Health: "HEALTH_OK", PGs: 33, CleanPGs: 30}, "ceph: 3 of 33 PGs are not active+clean", "ceph: 3 of 33 PGs are not active+clean"},
		"warn and not clean": {cephReport{Health: "HEALTH_WARN", PGs: 33, CleanPGs: 1}, "ceph: health is HEALTH_WARN", "ceph: 32 of 33 PGs are not active+clean"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := cephHealthProblem(tc.report); got != tc.health {
				t.Fatalf("health problem %q, want %q", got, tc.health)
			}
			if got := cephPGProblem(tc.report); got != tc.pgs {
				t.Fatalf("pg problem %q, want %q", got, tc.pgs)
			}
		})
	}
}

type recordedExec struct {
	namespace, pod, container string
	command                   []string
}

func fakeExec(out string, err error, calls *[]recordedExec) execFunc {
	return func(_ context.Context, namespace, pod, container string, command []string) ([]byte, error) {
		*calls = append(*calls, recordedExec{namespace, pod, container, command})
		return []byte(out), err
	}
}

func createRookOperatorPod(t *testing.T, ctx context.Context, c client.Client, name string, phase corev1.PodPhase) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: storageNamespace, Labels: map[string]string{"app": "rook-ceph-operator"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: rookOperatorContainer, Image: "docker.io/rook/ceph:v1.20.7"}}},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = phase
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
}

func createCephCluster(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ceph.rook.io/v1", "kind": "CephCluster", "metadata": map[string]any{"name": storageNamespace, "namespace": storageNamespace}}}
	if err := c.Create(ctx, obj); err != nil {
		t.Fatal(err)
	}
}

func TestReadCephSkipsWithoutTheCRD(t *testing.T) {
	c, _ := StartTestEnv(t)
	var calls []recordedExec
	got, err := readCeph(context.Background(), c, fakeExec(cephStatusJSON, nil, &calls))
	if err != nil || !got.Skipped || len(calls) != 0 {
		t.Fatalf("report %+v err %v calls %v", got, err, calls)
	}
}

func TestReadCeph(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: storageNamespace}}); err != nil {
		t.Fatal(err)
	}
	var calls []recordedExec
	if got, err := readCeph(ctx, c, fakeExec(cephStatusJSON, nil, &calls)); err != nil || !got.Skipped {
		t.Fatalf("no CephCluster: report %+v err %v", got, err)
	}
	createCephCluster(t, ctx, c)
	if _, err := readCeph(ctx, c, fakeExec(cephStatusJSON, nil, &calls)); err == nil || err.Error() != "no running rook-ceph-operator pod in rook-ceph" {
		t.Fatalf("no operator pod: %v", err)
	}
	createRookOperatorPod(t, ctx, c, "rook-ceph-operator-pending", corev1.PodPending)
	createRookOperatorPod(t, ctx, c, "rook-ceph-operator-running", corev1.PodRunning)
	got, err := readCeph(ctx, c, fakeExec(cephStatusJSON, nil, &calls))
	if err != nil || got != (cephReport{Health: "HEALTH_OK", PGs: 33, CleanPGs: 30}) {
		t.Fatalf("report %+v err %v", got, err)
	}
	want := recordedExec{storageNamespace, "rook-ceph-operator-running", rookOperatorContainer, []string{"ceph", "status", "--format", "json", "--connect-timeout=10", "--conf=/var/lib/rook/rook-ceph/rook-ceph.config"}}
	last := calls[len(calls)-1]
	if len(calls) != 1 || last.namespace != want.namespace || last.pod != want.pod || last.container != want.container || !slices.Equal(last.command, want.command) {
		t.Fatalf("exec calls %+v", calls)
	}
	if _, err := readCeph(ctx, c, fakeExec("", errors.New("connection refused"), &calls)); err == nil || err.Error() != "connection refused" {
		t.Fatalf("exec error: %v", err)
	}
}

func TestPodExecReachesTheExecSubresource(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: storageNamespace}}); err != nil {
		t.Fatal(err)
	}
	createRookOperatorPod(t, ctx, c, "rook-ceph-operator-unscheduled", corev1.PodRunning)
	exec, err := podExec(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = exec(ctx, storageNamespace, "rook-ceph-operator-unscheduled", rookOperatorContainer, cephCommand())
	want := "exec ceph status --format json --connect-timeout=10 --conf=/var/lib/rook/rook-ceph/rook-ceph.config in rook-ceph/rook-ceph-operator-unscheduled: unable to upgrade streaming request: pod rook-ceph-operator-unscheduled does not have a host assigned"
	if err == nil || err.Error() != want {
		t.Fatalf("exec error %q, want %q", err, want)
	}
}
