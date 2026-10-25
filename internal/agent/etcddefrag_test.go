package agent

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestDefragScheduleOffsetsMembersByFifteenMinutes(t *testing.T) {
	want := map[int]string{0: "0 3 * * 0", 1: "15 3 * * 0", 2: "30 3 * * 0", 3: "45 3 * * 0", 4: "0 3 * * 0", 7: "45 3 * * 0"}
	for index, expr := range want {
		if got := defragSchedule(index); got != expr {
			t.Errorf("index %d: got %q, want %q", index, got, expr)
		}
	}
}

func TestControllerIndexSortsControlPlaneHostsByName(t *testing.T) {
	hosts := []v1alpha1.Host{
		hostWithRoles("c", v1alpha1.RoleControlPlane),
		hostWithRoles("w", v1alpha1.RoleWorkload),
		hostWithRoles("a", v1alpha1.RoleControlPlane),
		hostWithRoles("b", v1alpha1.RoleControlPlane),
	}
	for name, want := range map[string]int{"a": 0, "b": 1, "c": 2} {
		if got, ok := controllerIndex(hosts, name); !ok || got != want {
			t.Errorf("%s: got %d %v, want %d", name, got, ok, want)
		}
	}
	if _, ok := controllerIndex(hosts, "w"); ok {
		t.Error("a workload host has no controller index")
	}
}

func TestDefragIsWorthItOnlyAboveTheReclaimThreshold(t *testing.T) {
	cases := []struct {
		size, inUse int64
		want        bool
	}{
		{1_900_000_000, 77_000_000, true},
		{268_435_456 + 100, 100, true},
		{268_435_456 + 99, 100, false},
		{100, 100, false},
		{50, 100, false},
	}
	for _, tc := range cases {
		if got := defragWorthIt(etcdStatus{DBSize: tc.size, DBSizeInUse: tc.inUse}); got != tc.want {
			t.Errorf("size %d inUse %d: got %v, want %v", tc.size, tc.inUse, got, tc.want)
		}
	}
}

type fakeEtcd struct {
	mu       sync.Mutex
	size     string
	inUse    string
	healthy  []string
	requests []string
	failHeal bool
}

func (f *fakeEtcd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	switch r.URL.Path {
	case "/health":
		if f.failHeal {
			w.Write([]byte(`{"health":"false"}`))
			return
		}
		w.Write([]byte(`{"health":"true"}`))
	case "/v3/maintenance/status":
		w.Write([]byte(`{"header":{"member_id":"1"},"dbSize":"` + f.size + `","dbSizeInUse":"` + f.inUse + `"}`))
	case "/v3/maintenance/defragment":
		w.Write([]byte(`{"header":{}}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeEtcd) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.requests, ",")
}

func TestDefragMemberChecksHealthDefragmentsAndChecksHealthAgain(t *testing.T) {
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	srv := httptest.NewServer(etcd)
	t.Cleanup(srv.Close)
	did, err := defragMember(context.Background(), srv.Client(), srv.URL)
	if err != nil || !did {
		t.Fatalf("did %v err %v", did, err)
	}
	want := "GET /health,POST /v3/maintenance/status,POST /v3/maintenance/defragment,GET /health"
	if got := etcd.seen(); got != want {
		t.Errorf("requests %s, want %s", got, want)
	}
}

func TestDefragMemberSkipsASmallReclaim(t *testing.T) {
	etcd := &fakeEtcd{size: "300000000", inUse: "200000000"}
	srv := httptest.NewServer(etcd)
	t.Cleanup(srv.Close)
	did, err := defragMember(context.Background(), srv.Client(), srv.URL)
	if err != nil || did {
		t.Fatalf("did %v err %v", did, err)
	}
	if strings.Contains(etcd.seen(), "defragment") {
		t.Errorf("requests %s", etcd.seen())
	}
}

func TestDefragMemberDoesNothingWhenTheMemberIsNotHealthy(t *testing.T) {
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000", failHeal: true}
	srv := httptest.NewServer(etcd)
	t.Cleanup(srv.Close)
	did, err := defragMember(context.Background(), srv.Client(), srv.URL)
	if err == nil || did {
		t.Fatalf("did %v err %v", did, err)
	}
	if etcd.seen() != "GET /health" {
		t.Errorf("requests %s", etcd.seen())
	}
}

func TestParseEtcdStatusReadsStringAndNumberSizes(t *testing.T) {
	for _, raw := range []string{`{"dbSize":"10","dbSizeInUse":"4"}`, `{"dbSize":10,"dbSizeInUse":4}`} {
		got, err := parseEtcdStatus([]byte(raw))
		if err != nil || got.DBSize != 10 || got.DBSizeInUse != 4 {
			t.Errorf("%s: %+v %v", raw, got, err)
		}
	}
	if _, err := parseEtcdStatus([]byte(`{"dbSize":"x"}`)); err == nil {
		t.Error("a bad size must fail")
	}
}

func writeEtcdPKI(t *testing.T, root string, srv *httptest.Server) {
	t.Helper()
	dir := filepath.Join(root, etcdPKIDir)
	if err := os.MkdirAll(filepath.Join(dir, "etcd"), 0o700); err != nil {
		t.Fatal(err)
	}
	cert := srv.TLS.Certificates[0]
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for path, data := range map[string][]byte{"apiserver-etcd-client.crt": certPEM, "apiserver-etcd-client.key": keyPEM, "etcd/ca.crt": certPEM} {
		if err := os.WriteFile(filepath.Join(dir, path), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func defragDeps(t *testing.T, srv *httptest.Server, now time.Time) Deps {
	t.Helper()
	deps := newDeps(nil, now)
	deps.Root = t.TempDir()
	deps.Node = "defrag-b"
	deps.EtcdEndpoint = srv.URL
	writeEtcdPKI(t, deps.Root, srv)
	return deps
}

func sundayAt(hour, minute int) time.Time {
	return time.Date(2026, 11, 1, hour, minute, 0, 0, time.UTC)
}

func TestRunEtcdDefragWaitsForTheScheduledSlotOfTheMember(t *testing.T) {
	createControlPlaneHost(t, "defrag-a", v1alpha1.RoleControlPlane)
	createControlPlaneHost(t, "defrag-b", v1alpha1.RoleControlPlane)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	srv := httptest.NewTLSServer(etcd)
	t.Cleanup(srv.Close)
	deps := defragDeps(t, srv, sundayAt(2, 0))
	if err := RunEtcdDefrag(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	deps.Now = func() time.Time { return sundayAt(3, 14) }
	if err := RunEtcdDefrag(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if etcd.seen() != "" {
		t.Fatalf("the member defragmented before its slot: %s", etcd.seen())
	}
	deps.Now = func() time.Time { return sundayAt(3, 16) }
	if err := RunEtcdDefrag(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(etcd.seen(), "POST /v3/maintenance/defragment") {
		t.Fatalf("no defragment: %s", etcd.seen())
	}
	etcd.mu.Lock()
	etcd.requests = nil
	etcd.mu.Unlock()
	deps.Now = func() time.Time { return sundayAt(3, 20) }
	if err := RunEtcdDefrag(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if etcd.seen() != "" {
		t.Fatalf("the member defragmented twice in a week: %s", etcd.seen())
	}
}

func TestRunEtcdDefragIgnoresAHostWithoutTheControlPlaneRole(t *testing.T) {
	createControlPlaneHost(t, "defrag-b", v1alpha1.RoleWorkload)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	srv := httptest.NewTLSServer(etcd)
	t.Cleanup(srv.Close)
	deps := defragDeps(t, srv, sundayAt(3, 16).AddDate(0, 0, -7))
	if err := RunEtcdDefrag(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	deps.Now = func() time.Time { return sundayAt(3, 16) }
	if err := RunEtcdDefrag(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if etcd.seen() != "" {
		t.Fatalf("requests %s", etcd.seen())
	}
}

func hostWithRoles(name string, roles ...string) v1alpha1.Host {
	return v1alpha1.Host{Spec: v1alpha1.HostSpec{Roles: roles}, ObjectMeta: metav1.ObjectMeta{Name: name}}
}
