package compute

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
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

func startManager(t *testing.T, cfg *rest.Config, lines *logLines) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
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
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
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

func TestSetupSkipsWithoutKubeVirtCRDs(t *testing.T) {
	c, cfg := startFreshEnv(t, testenv.CRDPaths()[:1])
	lines := &logLines{}
	startManager(t, cfg, lines)
	ns := uniqueNamespace(t, c)
	createVM(t, c, ns, "web-1", noMutation)
	time.Sleep(2 * time.Second)
	vm := getVM(t, c, ns, "web-1")
	if len(vm.Status.Conditions) != 0 || vm.Status.Phase != "" {
		t.Fatalf("status %+v", vm.Status)
	}
	if lines.matching("kubevirt.io/v1, Kind=VirtualMachine") != 1 {
		t.Fatalf("log lines %v", lines.lines)
	}
}
