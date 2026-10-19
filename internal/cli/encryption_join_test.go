package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

func clusterInRotation(phase string) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": v1alpha1.ClusterName}}}
	cluster.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Cluster"))
	if phase != "" {
		_ = unstructured.SetNestedField(cluster.Object, phase, "status", "encryption", "phase")
	}
	return cluster
}

func TestFetchEncryptionFileRefusesDuringARotation(t *testing.T) {
	content := testEncryptionContent(t)
	k0sToken, _ := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	var seen []*rest.Config
	got, err := FetchEncryptionFile(context.Background(), k0s.Token{K0sToken: k0sToken, VIP: "10.0.10.10"}, configClient(t, &seen, encryption.Secret(content), clusterInRotation(v1alpha1.RotationPromotingKey)))
	if err == nil || !strings.Contains(err.Error(), "key rotation is in progress") || got != nil {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestFetchEncryptionFileAfterARotationEnded(t *testing.T) {
	content := testEncryptionContent(t)
	k0sToken, _ := startJoinAPI(t, http.StatusOK, newJoinCA(t))
	var seen []*rest.Config
	got, err := FetchEncryptionFile(context.Background(), k0s.Token{K0sToken: k0sToken, VIP: "10.0.10.10"}, configClient(t, &seen, encryption.Secret(content), clusterInRotation("")))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("got %q %v", got, err)
	}
}
