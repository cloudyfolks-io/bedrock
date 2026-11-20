package operator

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestRunningOperatorBundleUsesTheNamedPodContainer(t *testing.T) {
	const image = "ghcr.io/cloudyfolks-io/bedrock:v1.0.0"
	pinned := image + "@sha256:" + strings.Repeat("a", 64)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-operator-test", Namespace: release.SystemNamespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Image: "registry.example/sidecar:1"},
			{Name: operatorContainer, Image: pinned},
		}},
	}
	var bundle release.Bundle
	bundle.Spec.Image = image
	scheme, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	have, err := runningOperatorBundle(context.Background(), reader, bundle, pod.Name)
	if err != nil || have.Spec.Image != pinned {
		t.Fatalf("running image %s, error %v", have.Spec.Image, err)
	}
	if _, err := runningOperatorBundle(context.Background(), reader, bundle, "missing"); err == nil {
		t.Fatal("accepted a missing operator pod")
	}
}
