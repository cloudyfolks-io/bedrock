package operator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/kinds"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var previousReleaseCRDs = []string{"clusterconfigs", "clusters", "hostconfigs", "hosts", "nodeupgrades", "releases", "settings"}

func TestSetupAreasRegistersWithAManager(t *testing.T) {
	_, cfg := StartTestEnv(t)
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := setupAreas(mgr, release.Bundle{}); err != nil {
		t.Fatal(err)
	}
}

func TestEveryBedrockKindHasACRD(t *testing.T) {
	_, cfg := StartTestEnv(t)
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	required := kinds.OfGroup(mgr.GetScheme(), v1alpha1.GroupVersion.Group)
	if missing, isMissing := kinds.FirstMissing(mgr.GetRESTMapper(), required); isMissing {
		t.Fatalf("no CRD serves %v", missing)
	}
}

func TestSetupAreasWaitsForTheKindsOfTheNewRelease(t *testing.T) {
	cfg := startEnvWithPreviousReleaseCRDs(t)
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := setupAreas(mgr, release.Bundle{}); err != nil {
		t.Fatalf("an operator that starts before its new CRDs must wait for them: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	if _, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{Paths: testenv.CRDPaths()[:1], ErrorIfPathMissing: true}); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: newTestScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	tenant := &v1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: "acme"},
		Spec: v1alpha1.TenantSpec{
			Owners:  []v1alpha1.SubjectRef{{Kind: v1alpha1.SubjectKindUser, Name: "alice"}},
			Network: v1alpha1.TenantNetworkSpec{VPCCIDR: "10.100.0.0/16"},
		},
	}
	if err := c.Create(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		select {
		case err := <-stopped:
			t.Fatalf("the manager stopped: %v", err)
		default:
		}
		return c.Get(ctx, client.ObjectKey{Name: "tenant:acme:owner"}, &rbacv1.ClusterRole{}) == nil, nil
	})
	if err != nil {
		t.Fatalf("the tenant controller did not start after the CRDs arrived: %v", err)
	}
}

func startEnvWithPreviousReleaseCRDs(t *testing.T) *rest.Config {
	t.Helper()
	dir := t.TempDir()
	for _, plural := range previousReleaseCRDs {
		name := "bedrock.cloudyfolks.io_" + plural + ".yaml"
		body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "00-crds", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     append([]string{dir}, testenv.CRDPaths()[1:]...),
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	return cfg
}
