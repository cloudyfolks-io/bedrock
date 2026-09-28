package release

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRemoteDigestReturnsIndexDigest(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref, err := name.ParseReference(host + "/lib/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := random.Index(64, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	want, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := RemoteDigest(context.Background(), ref.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != want.String() {
		t.Fatalf("digest %s, want %s", got, want)
	}
}

func TestResolveDigestsPinsAndSkips(t *testing.T) {
	resolve := func(_ context.Context, ref string) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	images := []string{
		"quay.io/jetstack/cert-manager-controller:v1.20.2",
		"ghcr.io/cloudyfolks-io/bedrock:v0.1.0",
		"registry.k8s.io/pause@sha256:" + strings.Repeat("b", 64),
	}
	pins, err := ResolveDigests(context.Background(), images, "ghcr.io/cloudyfolks-io/bedrock", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if got := pins["quay.io/jetstack/cert-manager-controller:v1.20.2"]; got != "quay.io/jetstack/cert-manager-controller:v1.20.2@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("pinned %q", got)
	}
	if _, ok := pins["ghcr.io/cloudyfolks-io/bedrock:v0.1.0"]; ok {
		t.Fatal("bedrock image must not be pinned")
	}
	if _, ok := pins["registry.k8s.io/pause@sha256:"+strings.Repeat("b", 64)]; ok {
		t.Fatal("digest references must not be resolved again")
	}
}

func TestResolveDigestsReportsFailure(t *testing.T) {
	resolve := func(_ context.Context, ref string) (string, error) {
		return "", errors.New("offline")
	}
	if _, err := ResolveDigests(context.Background(), []string{"quay.io/a/b:1"}, "", resolve); err == nil || !strings.Contains(err.Error(), "quay.io/a/b:1") {
		t.Fatalf("expected error naming the image, got %v", err)
	}
}

func TestPinImagesRewritesEveryImageField(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"template": map[string]any{"spec": map[string]any{
				"initContainers": []any{map[string]any{"image": "quay.io/a/init:1"}},
				"containers":     []any{map[string]any{"image": "quay.io/a/b:1"}, map[string]any{"image": "ghcr.io/cloudyfolks-io/bedrock:v1"}},
			}},
		},
	}}
	pins := map[string]string{"quay.io/a/b:1": "quay.io/a/b@sha256:x", "quay.io/a/init:1": "quay.io/a/init@sha256:y"}
	out := PinImages([]rendered{{group: "90-x", file: "d.yaml", objects: []*unstructured.Unstructured{obj}}}, pins, nil)
	got := ImagesOf([]Group{{Objects: out[0].objects}})
	want := []string{"ghcr.io/cloudyfolks-io/bedrock:v1", "quay.io/a/b@sha256:x", "quay.io/a/init@sha256:y"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
	if original := ImagesOf([]Group{{Objects: []*unstructured.Unstructured{obj}}}); original[1] != "quay.io/a/b:1" {
		t.Fatal("input must not be mutated")
	}
}
