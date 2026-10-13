package compute

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) add(prefix, args string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, prefix+" "+args)
}

func (l *logLines) matching(text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, line := range l.lines {
		if strings.Contains(line, text) {
			count++
		}
	}
	return count
}

type recordingCache struct {
	cache.Cache
	mu    sync.Mutex
	kinds []string
}

func (r *recordingCache) record(obj any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds = append(r.kinds, fmt.Sprintf("%T", obj))
}

func (r *recordingCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	r.record(obj)
	return r.Cache.GetInformer(ctx, obj, opts...)
}

func (r *recordingCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.record(obj)
	return r.Cache.Get(ctx, key, obj, opts...)
}

func (r *recordingCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.record(list)
	return r.Cache.List(ctx, list, opts...)
}

func (r *recordingCache) recorded(kind string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, seen := range r.kinds {
		if strings.Contains(seen, kind) {
			return true
		}
	}
	return false
}

func startManager(t *testing.T, cfg *rest.Config, lines *logLines) {
	t.Helper()
	startRecordedManager(t, cfg, lines, &recordingCache{})
}

func startRecordedManager(t *testing.T, cfg *rest.Config, lines *logLines, recorder *recordingCache) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		NewCache: func(cfg *rest.Config, opts cache.Options) (cache.Cache, error) {
			inner, err := cache.New(cfg, opts)
			recorder.Cache = inner
			return recorder, err
		},
		Scheme:     testScheme(t),
		Metrics:    server.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		Logger:     funcr.New(lines.add, funcr.Options{Verbosity: 1}),
	})
	must(t, err)
	must(t, SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 200*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		return check(), nil
	})
	if err != nil {
		t.Fatalf("timeout waiting for %s", what)
	}
}

func TestSetupStartsControllers(t *testing.T) {
	c, cfg := startFreshEnv(t, testenv.CRDPaths())
	startManager(t, cfg, &logLines{})
	ns := seedPlatform(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	eventually(t, "the KubeVirt VM and the Ready condition", func() bool {
		if _, err := getUnstructured(c, kubeVirtVMGVK, ns, "web-1"); err != nil {
			return false
		}
		var vm v1alpha1.VirtualMachine
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web-1"}, &vm); err != nil {
			return false
		}
		return meta.FindStatusCondition(vm.Status.Conditions, v1alpha1.ConditionReady) != nil
	})
	newVolume(t, c, ns, "extra", func(v *v1alpha1.Volume) {})
	eventually(t, "the volume claim and finalizer", func() bool {
		var volume v1alpha1.Volume
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "extra"}, &volume); err != nil {
			return false
		}
		_, err := getUnstructured(c, dataVolumeGVK, ns, "extra")
		return err == nil && containsString(volume.Finalizers, FinalizerVolumeInUse)
	})
	createObject(t, c, &v1alpha1.SSHKey{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bob"}, Spec: v1alpha1.SSHKeySpec{PublicKey: ed25519Key(t)}})
	eventually(t, "the SSH key condition", func() bool {
		var key v1alpha1.SSHKey
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "bob"}, &key); err != nil {
			return false
		}
		return v1alpha1.IsConditionTrue(key.Status.Conditions, v1alpha1.ConditionReady)
	})
}

func TestSetupRegistersNoSecretWatch(t *testing.T) {
	c, cfg := startFreshEnv(t, testenv.CRDPaths())
	recorder := &recordingCache{}
	startRecordedManager(t, cfg, &logLines{}, recorder)
	ns := seedPlatform(t, c)
	createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "init"}, Data: map[string][]byte{"userdata": []byte("#cloud-config\n")}})
	createVM(t, c, ns, "web-1", func(vm *v1alpha1.VirtualMachine) { vm.Spec.CloudInit = &v1alpha1.VMCloudInit{SecretRef: "init"} })
	eventually(t, "the cloud-init Secret", func() bool {
		return c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web-1-cloudinit"}, &corev1.Secret{}) == nil
	})
	eventually(t, "the informers", func() bool { return recorder.recorded("v1.Pod") })
	if recorder.recorded("v1.Secret") {
		t.Fatalf("a compute controller reads or watches Secrets through the cache: %v", recorder.kinds)
	}
}

func TestSetupSkipsWithoutKubeVirtCRDs(t *testing.T) {
	c, cfg := startFreshEnv(t, testenv.CRDPaths()[:1])
	lines := &logLines{}
	startManager(t, cfg, lines)
	ns := uniqueNamespace(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	eventually(t, "the skip log line", func() bool { return lines.matching("kubevirt.io/v1, Kind=VirtualMachine") == 1 })
	vm := getVM(t, c, ns, "web-1")
	if len(vm.Status.Conditions) != 0 || vm.Status.Phase != "" {
		t.Fatalf("status %+v", vm.Status)
	}
}

func TestSetupRegistersWhenKubeVirtCRDsArrive(t *testing.T) {
	c, cfg := startFreshEnv(t, testenv.CRDPaths()[:1])
	lines := &logLines{}
	startManager(t, cfg, lines)
	ns := uniqueNamespace(t, c)
	createObject(t, c, &v1alpha1.SSHKey{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bob"}, Spec: v1alpha1.SSHKeySpec{PublicKey: ed25519Key(t)}})
	eventually(t, "the wait log line", func() bool { return lines.matching("the controllers wait until the required kinds are served") == 1 })

	_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{Paths: testenv.CRDPaths()[1:], ErrorIfPathMissing: true})
	must(t, err)

	eventually(t, "the SSH key condition", func() bool {
		var key v1alpha1.SSHKey
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "bob"}, &key); err != nil {
			return false
		}
		return v1alpha1.IsConditionTrue(key.Status.Conditions, v1alpha1.ConditionReady)
	})
}
