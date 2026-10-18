package compute

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	sharedEnv    *envtest.Environment
	sharedOnce   sync.Once
	sharedConfig *rest.Config
	sharedErr    error
	sharedCancel context.CancelFunc
	ipCacheOnce  sync.Once
	ipCache      cache.Cache
	namespaceSeq int
	namespaceMu  sync.Mutex
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func startFreshEnv(t *testing.T, paths []string) (client.Client, *rest.Config) {
	t.Helper()
	env := &envtest.Environment{CRDDirectoryPaths: paths, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}

func startEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	sharedOnce.Do(func() {
		sharedEnv = &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
		sharedConfig, sharedErr = sharedEnv.Start()
		if sharedErr == nil {
			sharedConfig.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
		}
	})
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	c, err := client.New(sharedConfig, client.Options{Scheme: testScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c, rest.CopyConfig(sharedConfig)
}

func sharedFabricIPs() client.Reader {
	ipCacheOnce.Do(func() {
		scheme := runtime.NewScheme()
		if err := clientgoscheme.AddToScheme(scheme); err != nil {
			panic(err)
		}
		var err error
		if ipCache, err = cache.New(sharedConfig, cache.Options{Scheme: scheme}); err != nil {
			panic(err)
		}
		if err := network.IndexFabricIPs(context.Background(), ipCache); err != nil {
			panic(err)
		}
		var ctx context.Context
		ctx, sharedCancel = context.WithCancel(context.Background())
		go func() { _ = ipCache.Start(ctx) }()
		if !ipCache.WaitForCacheSync(ctx) {
			panic("the Fabric IP cache did not sync")
		}
	})
	return ipCache
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedCancel != nil {
		sharedCancel()
	}
	if sharedEnv != nil {
		_ = sharedEnv.Stop()
	}
	os.Exit(code)
}

func newProjectNamespace(t *testing.T, c client.Client, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"},
	}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
}

func uniqueNamespaceSuffix() string {
	namespaceMu.Lock()
	defer namespaceMu.Unlock()
	namespaceSeq++
	return strconv.Itoa(namespaceSeq)
}

func uniqueNamespace(t *testing.T, c client.Client) string {
	t.Helper()
	name := "acme-web-" + uniqueNamespaceSuffix()
	newProjectNamespace(t, c, name)
	return name
}

func fixtureVM(namespace, name string) v1alpha1.VirtualMachine {
	return v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1alpha1.VirtualMachineSpec{
			InstanceType: "u1.medium",
			Preference:   "ubuntu",
			Image:        &v1alpha1.ImageRef{Name: "ubuntu-24.04", Scope: v1alpha1.ImageScopeCluster},
			RootDisk:     v1alpha1.VMRootDisk{SizeGi: 40},
			Disks:        []v1alpha1.VMDisk{{VolumeRef: "data1"}},
			Networks:     []v1alpha1.VMNetwork{{SubnetRef: "default", SecurityGroups: []string{"web"}}},
			CloudInit:    &v1alpha1.VMCloudInit{UserData: "#cloud-config\nhostname: web-1\n"},
			SSHKeys:      []string{"alice"},
			Running:      true,
		},
	}
}

func gvk(group, version, kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
}

func TestThirdPartyKindsAreServed(t *testing.T) {
	c, _ := startEnv(t)
	kinds := []schema.GroupVersionKind{
		gvk("kubevirt.io", "v1", "VirtualMachine"),
		gvk("kubevirt.io", "v1", "VirtualMachineInstance"),
		gvk("kubevirt.io", "v1", "VirtualMachineInstanceMigration"),
		gvk("snapshot.kubevirt.io", "v1beta1", "VirtualMachineSnapshot"),
		gvk("instancetype.kubevirt.io", "v1beta1", "VirtualMachineClusterInstancetype"),
		gvk("instancetype.kubevirt.io", "v1beta1", "VirtualMachineClusterPreference"),
		gvk("cdi.kubevirt.io", "v1beta1", "DataVolume"),
		gvk("cdi.kubevirt.io", "v1beta1", "DataSource"),
		gvk("snapshot.storage.k8s.io", "v1", "VolumeSnapshot"),
		gvk("snapshot.storage.k8s.io", "v1", "VolumeSnapshotClass"),
		gvk("k8s.cni.cncf.io", "v1", "NetworkAttachmentDefinition"),
	}
	for _, kind := range kinds {
		if _, err := c.RESTMapper().RESTMapping(kind.GroupKind(), kind.Version); err != nil {
			t.Errorf("kind %s is not served: %v", kind, err)
		}
	}
	for _, kind := range []string{"VirtualMachine", "Volume", "VolumeSnapshot", "SSHKey", "Image", "ClusterImage", "Subnet", "SecurityGroup", "ElasticIP"} {
		if _, err := c.RESTMapper().RESTMapping(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: kind}, v1alpha1.GroupVersion.Version); err != nil {
			t.Errorf("bedrock kind %s is not served: %v", kind, err)
		}
	}
}
