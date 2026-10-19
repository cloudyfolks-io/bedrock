package network

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	envOnce   sync.Once
	envClient client.Client
	envConfig *rest.Config
	envServer *envtest.Environment
	envErr    error
	counter   atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if envServer != nil {
		_ = envServer.Stop()
	}
	stopChainEnv()
	os.Exit(code)
}

func testScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, v1alpha1.AddToScheme(scheme)
}

func startEnv(paths []string) (*envtest.Environment, client.Client, *rest.Config, error) {
	scheme, err := testScheme()
	if err != nil {
		return nil, nil, nil, err
	}
	server := &envtest.Environment{CRDDirectoryPaths: paths, ErrorIfCRDPathMissing: true}
	cfg, err := server.Start()
	if err != nil {
		return nil, nil, nil, err
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	return server, c, cfg, err
}

func testEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	envOnce.Do(func() {
		envServer, envClient, envConfig, envErr = startEnv(testenv.CRDPaths())
	})
	if envErr != nil {
		t.Fatal(envErr)
	}
	return envClient, envConfig
}

func unique(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s%d", prefix, counter.Add(1))
}

func newNamespace(t *testing.T, tenant, project string) string {
	t.Helper()
	c, _ := testEnv(t)
	return newNamespaceIn(t, c, tenant, project)
}

func newNamespaceIn(t *testing.T, c client.Client, tenant, project string) string {
	t.Helper()
	name := tenant + "-" + project
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{v1alpha1.LabelTenant: tenant, v1alpha1.LabelProject: project}}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestEnvironmentStartsWithAllCRDs(t *testing.T) {
	c, _ := testEnv(t)
	namespace := newNamespace(t, "suite", unique(t, "p"))
	if namespace == "" || c == nil {
		t.Fatal("namespace was not created")
	}
}

func setSetting(t *testing.T, key, value string) {
	t.Helper()
	c, _ := testEnv(t)
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}
	err := c.Get(ctx, client.ObjectKey{Name: key}, setting)
	switch {
	case apierrors.IsNotFound(err):
		setting.Spec.Value = value
		err = c.Create(ctx, setting)
	case err == nil:
		setting.Spec.Value = value
		err = c.Update(ctx, setting)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}) })
}

var purgedBedrockKinds = []string{"NetworkPeering", "Network", "Subnet", "ElasticIP", "LoadBalancer", "InternetGateway", "DNSZone", "SecurityGroup", "VirtualMachine", "Tenant"}

var purgedFabricKinds = []schema.GroupVersionKind{
	fabric.OvnSnatRuleGVK, fabric.OvnFipGVK, fabric.OvnEipGVK, fabric.RouterLBRuleGVK, fabric.SwitchLBRuleGVK, fabric.VipGVK, fabric.DNSZoneGVK, fabric.SecurityGroupGVK,
	fabric.SubnetGVK, fabric.VpcGVK, fabric.VlanGVK, fabric.ProviderNetworkGVK, fabric.BgpConfGVK, fabric.IPGVK,
}

func purgeKind(ctx context.Context, c client.Client, gvk schema.GroupVersionKind) {
	items, err := listObjects(ctx, c, gvk)
	if err != nil {
		return
	}
	for _, item := range items {
		if len(item.GetFinalizers()) > 0 {
			item.SetFinalizers(nil)
			_ = c.Update(ctx, item)
		}
		_ = c.Delete(ctx, item)
	}
}

func cleanEnv(t *testing.T) client.Client {
	t.Helper()
	c, _ := testEnv(t)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, kind := range purgedBedrockKinds {
			purgeKind(ctx, c, v1alpha1.GroupVersion.WithKind(kind))
		}
		for _, gvk := range purgedFabricKinds {
			purgeKind(ctx, c, gvk)
		}
		purgeKind(ctx, c, attachmentGVK)
		purgeKind(ctx, c, networkPolicyGVK)
	})
	return c
}

var networkPolicyGVK = schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
