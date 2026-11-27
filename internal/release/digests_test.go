package release

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestResolveDigestsResolvesUniqueRefsWithBoundedWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	images := make([]string, 0, digestWorkers+2)
	for i := range digestWorkers + 1 {
		images = append(images, fmt.Sprintf("quay.io/test/image:%d", i))
	}
	images = append(images, images[0])
	var lock sync.Mutex
	calls := make(map[string]int)
	active, peak := 0, 0
	started := make(chan struct{}, len(images))
	proceed := make(chan struct{})
	resolve := func(ctx context.Context, ref string) (string, error) {
		lock.Lock()
		calls[ref]++
		active++
		peak = max(peak, active)
		lock.Unlock()
		defer func() {
			lock.Lock()
			active--
			lock.Unlock()
		}()
		started <- struct{}{}
		select {
		case <-proceed:
			return "sha256:" + strings.Repeat("a", 64), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() {
		pins, err := ResolveDigests(ctx, images, "", resolve)
		if err == nil && len(pins) != len(images)-1 {
			err = fmt.Errorf("got %d pins, want %d", len(pins), len(images)-1)
		}
		done <- err
	}()
	for range digestWorkers {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("independent resolutions did not start together")
		}
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak != digestWorkers || len(calls) != len(images)-1 || calls[images[0]] != 1 {
		t.Fatalf("unexpected resolver peak %d or calls %v", peak, calls)
	}
}

func TestResolveDigestsKeepsInputErrorOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	first, second := "quay.io/test/image:1", "quay.io/test/image:2"
	firstErr, secondErr := errors.New("first failure"), errors.New("second failure")
	startedSecond := make(chan struct{})
	resolve := func(ctx context.Context, ref string) (string, error) {
		if ref == second {
			close(startedSecond)
			return "", secondErr
		}
		select {
		case <-startedSecond:
			return "", firstErr
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	pins, err := ResolveDigests(ctx, []string{first, second}, "", resolve)
	if pins != nil || !errors.Is(err, firstErr) || !strings.Contains(err.Error(), first) {
		t.Fatalf("got pins %v and error %v, want first ref failure", pins, err)
	}
}

func TestResolveDigestsBoundsRefDeadline(t *testing.T) {
	ref := "quay.io/test/image:1"
	resolve := func(ctx context.Context, _ string) (string, error) {
		deadline, found := ctx.Deadline()
		if !found || time.Until(deadline) > digestTimeout {
			return "", errors.New("missing per-ref deadline")
		}
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	if _, err := ResolveDigests(t.Context(), []string{ref}, "", resolve); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	wait := func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	pins, err := ResolveDigests(ctx, []string{ref}, "", wait)
	if pins != nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), ref) {
		t.Fatalf("got pins %v and error %v, want named deadline error", pins, err)
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
