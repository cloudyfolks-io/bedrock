package proxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

const tokenFileContent = `token-admin,admin,uid-admin,"system:masters"
token-alice,bedrock:alice,uid-alice,"bedrock:ops"
token-bob,bedrock:bob,uid-bob
jwt.shaped.alice,bedrock:alice,uid-alice-jwt,"bedrock:ops"
`

var (
	envOnce    sync.Once
	envErr     error
	testEnv    *envtest.Environment
	testCfg    *rest.Config
	testClient client.Client
	tokenDir   string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testEnv != nil {
		_ = testEnv.Stop()
	}
	if tokenDir != "" {
		_ = os.RemoveAll(tokenDir)
	}
	os.Exit(code)
}

func startEnv(t *testing.T) client.Client {
	t.Helper()
	envOnce.Do(func() { envErr = bootEnv() })
	if envErr != nil {
		t.Fatal(envErr)
	}
	return testClient
}

func bootEnv() error {
	dir, err := os.MkdirTemp("", "proxy-tokens-")
	if err != nil {
		return err
	}
	tokenDir = dir
	tokenFile := filepath.Join(dir, "tokens.csv")
	if err := os.WriteFile(tokenFile, []byte(tokenFileContent), 0o600); err != nil {
		return err
	}
	testEnv = &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
	testEnv.ControlPlane.GetAPIServer().Configure().Append("token-auth-file", tokenFile)
	cfg, err := testEnv.Start()
	if err != nil {
		return err
	}
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	testCfg = cfg
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	testClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	for _, name := range []string{StoreNamespace, "bedrock-system"} {
		if err := testClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			return fmt.Errorf("create namespace %s: %w", name, err)
		}
	}
	return nil
}

func restConfigFor(token string) *rest.Config {
	return &rest.Config{
		Host:            testCfg.Host,
		TLSClientConfig: rest.TLSClientConfig{CAData: testCfg.CAData},
		BearerToken:     token,
		QPS:             -1,
	}
}
