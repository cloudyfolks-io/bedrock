package container

import (
	"context"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
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

func testScheme(t testing.TB) *runtime.Scheme {
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

func startEnv(t testing.TB) (client.Client, *rest.Config) {
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
		var err error
		if ipCache, err = cache.New(sharedConfig, cache.Options{Scheme: runtime.NewScheme()}); err != nil {
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

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func createObject(t testing.TB, c client.Client, obj client.Object) {
	t.Helper()
	must(t, c.Create(context.Background(), obj))
}

func updateStatus[T client.Object](t testing.TB, c client.Client, obj T, mutate func(T)) {
	t.Helper()
	ctx := context.Background()
	must(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		mutate(obj)
		return c.Status().Update(ctx, obj)
	}))
}

func readyConditions(status metav1.ConditionStatus) []metav1.Condition {
	return []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: status, Reason: "Test", LastTransitionTime: metav1.Now()}}
}

func uniqueNamespaceSuffix() string {
	namespaceMu.Lock()
	defer namespaceMu.Unlock()
	namespaceSeq++
	return strconv.Itoa(namespaceSeq)
}

func uniqueNamespace(t testing.TB, c client.Client) string {
	t.Helper()
	name := "acme-web-" + uniqueNamespaceSuffix()
	createObject(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"},
	}})
	return name
}

func ensureNetwork(t testing.TB, c client.Client) {
	t.Helper()
	err := c.Create(context.Background(), &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDRs: []string{"10.100.0.0/16"}}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	network := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}
	updateStatus(t, c, network, func(n *v1alpha1.Network) { n.Status.VPC = "acme" })
}

func createSubnet(t testing.TB, c client.Client, namespace, name string, ready metav1.ConditionStatus) {
	t.Helper()
	subnet := &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.0.0/24"},
	}
	createObject(t, c, subnet)
	updateStatus(t, c, subnet, func(s *v1alpha1.Subnet) { s.Status.Conditions = readyConditions(ready) })
}

func createSecurityGroup(t testing.TB, c client.Client, namespace, name string, ready metav1.ConditionStatus) {
	t.Helper()
	group := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	createObject(t, c, group)
	updateStatus(t, c, group, func(g *v1alpha1.SecurityGroup) { g.Status.Conditions = readyConditions(ready) })
}

func ensureDefaultGroup(t testing.TB, c client.Client, namespace string) {
	t.Helper()
	group := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": network.DefaultSecurityGroupName(namespace)}, "spec": map[string]any{}}}
	group.SetGroupVersionKind(fabricGroupGVK)
	if err := c.Create(context.Background(), group); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func ensureRuntimeClass(t testing.TB, c client.Client, name string) {
	t.Helper()
	err := c.Create(context.Background(), &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Handler: name})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func setRuntimeSetting(t testing.TB, c client.Client, value string) {
	t.Helper()
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: RuntimeSetting}}
	err := c.Get(ctx, client.ObjectKeyFromObject(setting), setting)
	setting.Spec.Value = value
	if apierrors.IsNotFound(err) {
		must(t, c.Create(ctx, setting))
	} else {
		must(t, err)
		must(t, c.Update(ctx, setting))
	}
	t.Cleanup(func() { _ = c.Delete(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: RuntimeSetting}}) })
}

func createVolume(t testing.TB, c client.Client, namespace, name, class string) {
	t.Helper()
	createObject(t, c, &v1alpha1.Volume{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.VolumeSpec{SizeGi: 10, StorageClass: class},
	})
}

func createPullSecret(t testing.TB, c client.Client, namespace, name string) {
	t.Helper()
	createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte("{}")}})
}

func seedPlatform(t testing.TB, c client.Client) string {
	t.Helper()
	ns := uniqueNamespace(t, c)
	seedPlatformIn(t, c, ns)
	return ns
}

func seedPlatformIn(t testing.TB, c client.Client, ns string) {
	t.Helper()
	ensureNetwork(t, c)
	createSubnet(t, c, ns, "default", metav1.ConditionTrue)
	createSecurityGroup(t, c, ns, "web", metav1.ConditionTrue)
	ensureDefaultGroup(t, c, ns)
	ensureRuntimeClass(t, c, "kata")
}

func fixtureContainer(namespace, name string) v1alpha1.Container {
	return v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.ContainerSpec{Image: "registry.test/web:1", Network: v1alpha1.ContainerNetwork{SubnetRef: "default"}},
	}
}

func createContainer(t testing.TB, c client.Client, namespace, name string, mutate func(*v1alpha1.ContainerSpec)) v1alpha1.Container {
	t.Helper()
	container := fixtureContainer(namespace, name)
	mutate(&container.Spec)
	createObject(t, c, &container)
	return container
}

func noMutation(*v1alpha1.ContainerSpec) {}

func createFabricIP(t testing.TB, c client.Client, name, namespace, pod, subnet, address string) {
	t.Helper()
	must(t, fabrictest.CreateIP(context.Background(), c, name, namespace, pod, subnet, address))
	eventually(t, "the cache to list the Fabric IP "+name, func() bool { return cachedFabricIP(name) })
}

func cachedFabricIP(name string) bool {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(fabric.IPGVK.GroupVersion().WithKind("IPList"))
	if err := sharedFabricIPs().List(context.Background(), list); err != nil {
		return false
	}
	return slices.ContainsFunc(list.Items, func(ip unstructured.Unstructured) bool { return ip.GetName() == name })
}

func newResolver(c client.Client) Resolver {
	return Resolver{Client: c, FabricIPs: sharedFabricIPs()}
}

func eventually(t testing.TB, what string, check func() bool) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 200*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		return check(), nil
	})
	if err != nil {
		t.Fatalf("timeout waiting for %s", what)
	}
}

func corev1Namespace(labels map[string]string) corev1.Namespace {
	return corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns", Labels: labels}}
}

func podOf(namespace, name, container string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: SelectorLabels(container)},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.test/web:1"}}},
	}
}

func ensureOwnerChain(t testing.TB, c client.Client, namespace, container string) *appsv1.ReplicaSet {
	t.Helper()
	ctx := context.Background()
	var owner v1alpha1.Container
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: container}, &owner))
	labels := map[string]string{"app": container}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.test/web:1"}}}}
	controlled := func(kind string, uid types.UID, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "x/v1", Kind: kind, Name: name, UID: uid, Controller: ptr.To(true)}}
	}
	var deployment appsv1.Deployment
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: container}, &deployment)
	if apierrors.IsNotFound(err) {
		deployment = appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: container, OwnerReferences: controlled("Container", owner.UID, owner.Name)},
			Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
		}
		must(t, c.Create(ctx, &deployment))
	} else {
		must(t, err)
	}
	var set appsv1.ReplicaSet
	err = c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: container + "-rs"}, &set)
	if apierrors.IsNotFound(err) {
		set = appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: container + "-rs", Labels: map[string]string{v1alpha1.LabelKind: "Container", v1alpha1.LabelName: container}, OwnerReferences: controlled("Deployment", deployment.UID, deployment.Name)},
			Spec:       appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
		}
		must(t, c.Create(ctx, &set))
	} else {
		must(t, err)
	}
	return &set
}

func createPod(t testing.TB, c client.Client, namespace, name, container string) corev1.Pod {
	t.Helper()
	pod := createLoosePod(t, c, namespace, name, container, ensureOwnerChain(t, c, namespace, container))
	return pod
}

func createLoosePod(t testing.TB, c client.Client, namespace, name, container string, set *appsv1.ReplicaSet) corev1.Pod {
	t.Helper()
	err := c.Create(context.Background(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "default"}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	pod := podOf(namespace, name, container)
	if set != nil {
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}
	}
	createObject(t, c, &pod)
	return pod
}

func newRegistry() *proxy.Subresources {
	return proxy.NewSubresources()
}
