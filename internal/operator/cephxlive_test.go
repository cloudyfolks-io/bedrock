package operator

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func liveClient(cluster map[string]any) client.Client {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-operator-1", Namespace: storageNamespace, Labels: map[string]string{"app": "rook-ceph-operator"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	builder := fake.NewClientBuilder().WithObjects(pod)
	if cluster != nil {
		builder = builder.WithObjects(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "ceph.rook.io/v1", "kind": "CephCluster", "metadata": map[string]any{"name": storageNamespace, "namespace": storageNamespace}, "spec": cluster["spec"], "status": cluster["status"]}})
	}
	return builder.Build()
}

func rotatedStatus() map[string]any {
	return map[string]any{"cephx": map[string]any{
		"mon": map[string]any{"keyGeneration": int64(2)}, "mgr": map[string]any{"keyGeneration": int64(2)}, "osd": map[string]any{"keyGeneration": int64(2)},
		"csi": map[string]any{"keyGeneration": int64(3), "keyType": "aes256k"},
	}}
}

func TestReadLiveCephxWithoutACephCluster(t *testing.T) {
	live, err := readLiveCephx(context.Background(), liveClient(nil), nil, "aes256k")
	if err != nil || live != (LiveCephx{}) {
		t.Fatalf("live %+v err %v", live, err)
	}
}

func TestReadLiveCephxSeesTheRestriction(t *testing.T) {
	cluster := map[string]any{"spec": map[string]any{"security": map[string]any{"cephx": map[string]any{"allowedCiphers": []any{"aes256k"}, "csi": map[string]any{"keyType": "aes256k"}}}}}
	live, err := readLiveCephx(context.Background(), liveClient(cluster), nil, "aes256k")
	if err != nil || !live.Exists || !live.Restricted || !live.Aes256kKeys || live.Rotated {
		t.Fatalf("live %+v err %v", live, err)
	}
}

func TestReadLiveCephxProvesTheRotationWithTheKeyDump(t *testing.T) {
	cluster := map[string]any{"spec": map[string]any{}, "status": rotatedStatus()}
	var calls []recordedExec
	dump := `{"mon.":{"key":"AQ","type":"aes256k"}}`
	live, err := readLiveCephx(context.Background(), liveClient(cluster), fakeExec(dump, nil, &calls), "aes256k")
	if err != nil || !live.Rotated || live.RotationProblem != "" || len(calls) != 1 {
		t.Fatalf("live %+v err %v calls %v", live, err, calls)
	}
	live, _ = readLiveCephx(context.Background(), liveClient(cluster), fakeExec(`{"mon.":{"type":"aes"}}`, nil, &calls), "aes256k")
	if live.Rotated || live.RotationProblem == "" {
		t.Fatalf("an aes key must not prove it: %+v", live)
	}
	live, _ = readLiveCephx(context.Background(), liveClient(cluster), fakeExec("", errors.New("boom"), &calls), "aes256k")
	if live.Rotated || live.RotationProblem == "" {
		t.Fatalf("a failed read must not prove it: %+v", live)
	}
}

func TestReadLiveCephxDoesNotDumpKeysWhileTheStatusIsBehind(t *testing.T) {
	cluster := map[string]any{"spec": map[string]any{}, "status": map[string]any{"cephx": map[string]any{"mon": map[string]any{"keyGeneration": int64(1)}}}}
	var calls []recordedExec
	live, err := readLiveCephx(context.Background(), liveClient(cluster), fakeExec("", nil, &calls), "aes256k")
	if err != nil || live.Rotated || live.RotationProblem == "" || len(calls) != 0 {
		t.Fatalf("live %+v err %v calls %v", live, err, calls)
	}
}

func TestReadLiveCephxIgnoresTheRotationInAesMode(t *testing.T) {
	cluster := map[string]any{"spec": map[string]any{}, "status": rotatedStatus()}
	var calls []recordedExec
	live, err := readLiveCephx(context.Background(), liveClient(cluster), fakeExec("", nil, &calls), "aes")
	if err != nil || live.Rotated || len(calls) != 0 {
		t.Fatalf("live %+v err %v calls %v", live, err, calls)
	}
}
