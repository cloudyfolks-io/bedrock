//go:build e2e

package tenant

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	bedrockGroupVersion = "bedrock.cloudyfolks.io/v1alpha1"
	requestTimeout      = time.Minute
	pollInterval        = 3 * time.Second
)

var (
	bedrockVirtualMachine = schema.GroupVersionKind{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "VirtualMachine"}
	bedrockSetting        = schema.GroupVersionKind{Group: "bedrock.cloudyfolks.io", Version: "v1alpha1", Kind: "Setting"}
	kubevirtInstance      = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
	kubevirtMigration     = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstanceMigration"}
	kubevirtSnapshot      = schema.GroupVersionKind{Group: "snapshot.kubevirt.io", Version: "v1beta1", Kind: "VirtualMachineSnapshot"}
)

type env struct {
	host      string
	namespace string
	vm        string
	roots     *x509.CertPool
	k8s       client.Client
	http      *http.Client
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is not set", name)
	}
	return value
}

func certPool(t *testing.T, caFile string) *x509.CertPool {
	t.Helper()
	ca, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read %s: %v", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatalf("%s holds no certificate", caFile)
	}
	return pool
}

func trustingTransport(roots *x509.CertPool) *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
}

func noRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func kubeClient(t *testing.T) client.Client {
	t.Helper()
	restConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: runtime.NewScheme()})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

func setupEnv(t *testing.T) env {
	t.Helper()
	roots := certPool(t, requireEnv(t, "BEDROCK_CA"))
	return env{
		host:      requireEnv(t, "BEDROCK_HOST"),
		namespace: os.Getenv("NS"),
		vm:        os.Getenv("VM"),
		roots:     roots,
		k8s:       kubeClient(t),
		http:      &http.Client{Timeout: requestTimeout, Transport: trustingTransport(roots), CheckRedirect: noRedirects},
	}
}

func (e env) apiURL(path string) string {
	return "https://api." + e.host + path
}

func (e env) consoleURL(path string) string {
	return "https://console." + e.host + path
}

func (e env) vmPath(subresource string) string {
	return "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/" + e.namespace + "/virtualmachines/" + e.vm + "/" + subresource
}

func (e env) bearer(t *testing.T, token, method, rawURL string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request %s: %v", rawURL, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return e.send(t, req)
}

func (e env) send(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", req.Method, req.URL, err)
	}
	return resp.StatusCode, body
}

func requireStatus(t *testing.T, what string, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d: %s", what, got, want, body)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, check func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, observed := check()
		if ok {
			return
		}
		last = observed
		time.Sleep(pollInterval)
	}
	t.Fatalf("timeout after %s waiting for %s; last observed: %s", timeout, what, last)
}

func objectOf(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}

func (e env) fetch(gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	obj := objectOf(gvk)
	err := e.k8s.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
	return obj, err
}

func (e env) vmField(fields ...string) string {
	obj, err := e.fetch(bedrockVirtualMachine, e.namespace, e.vm)
	if err != nil {
		return ""
	}
	value, _, _ := unstructured.NestedString(obj.Object, fields...)
	return value
}

func (e env) waitVMPhase(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, "VirtualMachine "+e.vm+" phase "+want, func() (bool, string) {
		phase := e.vmField("status", "phase")
		return phase == want, phase
	})
}

func (e env) instanceUID() string {
	obj, err := e.fetch(kubevirtInstance, e.namespace, e.vm)
	if err != nil {
		return ""
	}
	return string(obj.GetUID())
}
