package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var errRecordedOnly = errors.New("the informer is only recorded")

type informerRecorder struct {
	cache.Cache
	scheme    *runtime.Scheme
	mu        sync.Mutex
	requested map[schema.GroupVersionKind]bool
}

func (r *informerRecorder) record(obj client.Object) error {
	gvk, err := apiutil.GVKForObject(obj, r.scheme)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requested[gvk] = true
	return nil
}

func (r *informerRecorder) GetInformer(_ context.Context, obj client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	if err := r.record(obj); err != nil {
		return nil, err
	}
	return nil, errRecordedOnly
}

func (r *informerRecorder) GetInformerForKind(_ context.Context, gvk schema.GroupVersionKind, _ ...cache.InformerGetOption) (cache.Informer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requested[gvk] = true
	return nil, errRecordedOnly
}

func (r *informerRecorder) IndexField(_ context.Context, obj client.Object, _ string, _ client.IndexerFunc) error {
	return r.record(obj)
}

func (r *informerRecorder) kinds() []schema.GroupVersionKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]schema.GroupVersionKind, 0, len(r.requested))
	for gvk := range r.requested {
		out = append(out, gvk)
	}
	return out
}

type recordingManager struct {
	ctrl.Manager
	informers *informerRecorder
	runnables *[]manager.Runnable
}

func (m recordingManager) GetCache() cache.Cache { return m.informers }

func (m recordingManager) GetFieldIndexer() client.FieldIndexer { return m.informers }

func (m recordingManager) GetRESTMapper() meta.RESTMapper {
	return servedMapper{m.Manager.GetRESTMapper()}
}

func (m recordingManager) Add(runnable manager.Runnable) error {
	*m.runnables = append(*m.runnables, runnable)
	return nil
}

type servedMapper struct{ meta.RESTMapper }

func (servedMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return &meta.RESTMapping{}, nil
}

func cachedKindsOfTheOperator(t *testing.T) []schema.GroupVersionKind {
	t.Helper()
	scheme, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	opts := managerOptions(scheme)
	opts.LeaderElection = false
	opts.Metrics = server.Options{BindAddress: "0"}
	opts.Controller = config.Controller{SkipNameValidation: ptr.To(true)}
	cfg := &rest.Config{Host: "https://127.0.0.1:1"}
	mgr, err := ctrl.NewManager(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	informers := &informerRecorder{Cache: mgr.GetCache(), scheme: scheme, requested: map[schema.GroupVersionKind]bool{}}
	var runnables []manager.Runnable
	if err := setupControllers(recordingManager{Manager: mgr, informers: informers, runnables: &runnables}, cfg, rbacBundle(t)); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	var running sync.WaitGroup
	for _, runnable := range runnables {
		running.Add(1)
		go func() {
			defer running.Done()
			_ = runnable.Start(ctx)
		}()
	}
	time.Sleep(2 * time.Second)
	stop()
	running.Wait()
	return informers.kinds()
}

func committedOperatorRole(t *testing.T) rbacv1.ClusterRole {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &role); err != nil {
		t.Fatal(err)
	}
	return role
}

func vendoredCRDPlurals(t *testing.T) map[schema.GroupKind]string {
	t.Helper()
	var objects []*unstructured.Unstructured
	for _, dir := range testenv.CRDPaths() {
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			object := &unstructured.Unstructured{}
			if err := sigyaml.Unmarshal(body, &object.Object); err != nil {
				t.Fatal(err)
			}
			objects = append(objects, object)
		}
	}
	return crdPlurals(objects)
}

func TestEveryKindThatTheControllersCacheHasListAndWatchInTheOperatorRole(t *testing.T) {
	cached := cachedKindsOfTheOperator(t)
	for _, anchor := range []schema.GroupVersionKind{
		{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "Network"},
		{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "VirtualMachine"},
		{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "Container"},
		{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "ObjectStorageBucket"},
		{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "KubernetesCluster"},
		{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "Addon"},
		{Version: "v1", Kind: "Pod"},
		{Version: "v1", Kind: "Namespace"},
	} {
		if !slices.Contains(cached, anchor) {
			t.Fatalf("the real setup did not register %s: %v", anchor, cached)
		}
	}
	role := committedOperatorRole(t)
	plurals := vendoredCRDPlurals(t)
	for _, gvk := range cached {
		if gvk.GroupKind() == (schema.GroupKind{Kind: "Secret"}) {
			t.Error("a controller caches Secrets: the operator never lists or watches them")
			continue
		}
		resource := resourceOf(gvk.GroupKind(), plurals)
		for _, verb := range []string{"list", "watch"} {
			if !allows(role, gvk.Group, resource, verb) {
				t.Errorf("a controller caches %s, and the generated operator role lacks %s on %s/%s (run the release rbac command)", gvk, verb, gvk.Group, resource)
			}
		}
	}
}
