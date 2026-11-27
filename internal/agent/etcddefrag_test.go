package agent

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestDefragSlotStartIsSundayThreeUTCPlusFifteenMinutesPerIndex(t *testing.T) {
	wednesday := time.Date(2026, 10, 28, 17, 45, 0, 0, time.UTC)
	want := map[int]time.Time{
		0: sundayAt(3, 0).AddDate(0, 0, -7),
		1: sundayAt(3, 15).AddDate(0, 0, -7),
		2: sundayAt(3, 30).AddDate(0, 0, -7),
		3: sundayAt(3, 45).AddDate(0, 0, -7),
		4: sundayAt(3, 0).AddDate(0, 0, -7),
		7: sundayAt(3, 45).AddDate(0, 0, -7),
	}
	for index, slot := range want {
		if got := defragSlotStart(wednesday, index); !got.Equal(slot) {
			t.Errorf("index %d: got %s, want %s", index, got, slot)
		}
	}
	if got := defragSlotStart(sundayAt(2, 59), 0); !got.Equal(sundayAt(3, 0).AddDate(0, 0, -7)) {
		t.Errorf("before the slot the slot of the last week counts: %s", got)
	}
	if got := defragSlotStart(sundayAt(3, 0), 0); !got.Equal(sundayAt(3, 0)) {
		t.Errorf("at the start: %s", got)
	}
}

func TestInDefragSlotLastsFifteenMinutes(t *testing.T) {
	cases := []struct {
		now  time.Time
		want bool
	}{
		{sundayAt(3, 14), false},
		{sundayAt(3, 15), true},
		{sundayAt(3, 29), true},
		{sundayAt(3, 30), false},
		{sundayAt(3, 15).AddDate(0, 0, 1), false},
		{sundayAt(3, 15).AddDate(0, 0, -1), false},
	}
	for _, tc := range cases {
		if got := inDefragSlot(tc.now, 1); got != tc.want {
			t.Errorf("%s: got %v", tc.now, got)
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
	extra    []string
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
	case "/v3/cluster/member/list":
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		urls := append([]string{scheme + "://" + r.Host}, f.extra...)
		members := make([]string, len(urls))
		for i, url := range urls {
			members[i] = `{"ID":"` + strconv.Itoa(i+1) + `","name":"m` + strconv.Itoa(i+1) + `","clientURLs":["` + url + `"]}`
		}
		w.Write([]byte(`{"members":[` + strings.Join(members, ",") + `]}`))
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

func defragDeps(t *testing.T, srv *httptest.Server, node string, now time.Time) Deps {
	t.Helper()
	deps := newDeps(nil, now)
	deps.Root = t.TempDir()
	deps.Node = node
	deps.EtcdEndpoint = srv.URL
	writeEtcdPKI(t, deps.Root, srv)
	return deps
}

func sundayAt(hour, minute int) time.Time {
	return time.Date(2026, 11, 1, hour, minute, 0, 0, time.UTC)
}

func runAt(t *testing.T, deps *Deps, now time.Time) error {
	t.Helper()
	deps.Now = func() time.Time { return now }
	return RunEtcdDefrag(context.Background(), k8sClient, *deps)
}

func startEtcd(t *testing.T, etcd *fakeEtcd) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(etcd)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeEtcd) defragments() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if strings.HasSuffix(request, "/defragment") {
			count++
		}
	}
	return count
}

func controllers(t *testing.T) {
	t.Helper()
	for _, name := range []string{"defrag-a", "defrag-b", "defrag-c"} {
		createControlPlaneHost(t, name, v1alpha1.RoleControlPlane)
	}
}

func TestRunEtcdDefragRunsOnceInsideTheSlotOfTheMember(t *testing.T) {
	controllers(t)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(2, 0))
	for _, now := range []time.Time{sundayAt(2, 0), sundayAt(3, 14), sundayAt(3, 30), sundayAt(9, 0)} {
		if err := runAt(t, &deps, now); err != nil {
			t.Fatal(err)
		}
	}
	if etcd.defragments() != 0 {
		t.Fatalf("defragmented outside the slot: %s", etcd.seen())
	}
	for _, now := range []time.Time{sundayAt(3, 16), sundayAt(3, 20), sundayAt(3, 29)} {
		if err := runAt(t, &deps, now); err != nil {
			t.Fatal(err)
		}
	}
	if etcd.defragments() != 1 {
		t.Fatalf("want one defragment in the slot: %s", etcd.seen())
	}
}

func TestRunEtcdDefragDoesNotCatchUpAMissedSlot(t *testing.T) {
	controllers(t)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(4, 0))
	for _, now := range []time.Time{sundayAt(4, 0), sundayAt(4, 0).AddDate(0, 0, 1), sundayAt(4, 0).AddDate(0, 0, 6), sundayAt(3, 14).AddDate(0, 0, 7)} {
		if err := runAt(t, &deps, now); err != nil {
			t.Fatal(err)
		}
	}
	if etcd.defragments() != 0 {
		t.Fatalf("caught up a missed slot: %s", etcd.seen())
	}
	if err := runAt(t, &deps, sundayAt(3, 20).AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	if etcd.defragments() != 1 {
		t.Fatalf("the slot of the next week must run: %s", etcd.seen())
	}
}

func TestRunEtcdDefragNeverRunsMembersTogether(t *testing.T) {
	controllers(t)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	srv := startEtcd(t, etcd)
	deps := map[string]*Deps{}
	for _, name := range []string{"defrag-a", "defrag-b", "defrag-c"} {
		d := defragDeps(t, srv, name, sundayAt(3, 0))
		deps[name] = &d
	}
	for now, runner := range map[time.Time]string{sundayAt(3, 5): "defrag-a", sundayAt(3, 20): "defrag-b", sundayAt(3, 35): "defrag-c"} {
		before := etcd.defragments()
		for name, d := range deps {
			if err := runAt(t, d, now); err != nil {
				t.Fatal(err)
			}
			ran := etcd.defragments() > before
			if ran != (name == runner) {
				t.Fatalf("at %s %s ran=%v", now, name, ran)
			}
			if ran {
				before = etcd.defragments()
			}
		}
	}
	if etcd.defragments() != 3 {
		t.Fatalf("defragments %d", etcd.defragments())
	}
	for _, d := range deps {
		if err := runAt(t, d, sundayAt(3, 50)); err != nil {
			t.Fatal(err)
		}
	}
	if etcd.defragments() != 3 {
		t.Fatalf("a member ran after its slot: %d", etcd.defragments())
	}
}

func TestRunEtcdDefragSkipsTheWeekWhenTheMemberIsNotHealthy(t *testing.T) {
	controllers(t)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000", failHeal: true}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(3, 0))
	if err := runAt(t, &deps, sundayAt(3, 16)); err == nil {
		t.Fatal("an unhealthy member must be reported")
	}
	etcd.mu.Lock()
	etcd.failHeal = false
	etcd.requests = nil
	etcd.mu.Unlock()
	for _, now := range []time.Time{sundayAt(3, 17), sundayAt(3, 29), sundayAt(3, 40), sundayAt(5, 0)} {
		if err := runAt(t, &deps, now); err != nil {
			t.Fatal(err)
		}
	}
	if etcd.seen() != "" {
		t.Fatalf("retried in the same week: %s", etcd.seen())
	}
	if err := runAt(t, &deps, sundayAt(3, 16).AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	if etcd.defragments() != 1 {
		t.Fatalf("next week must run: %s", etcd.seen())
	}
}

func TestRunEtcdDefragIgnoresClockJumps(t *testing.T) {
	controllers(t)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(3, 0))
	if err := runAt(t, &deps, sundayAt(3, 20)); err != nil {
		t.Fatal(err)
	}
	if etcd.defragments() != 1 {
		t.Fatalf("setup: %s", etcd.seen())
	}
	jumps := []time.Time{
		sundayAt(3, 16),
		sundayAt(3, 16).AddDate(0, 0, -7),
		sundayAt(3, 16).AddDate(0, -3, 0),
		sundayAt(3, 16).AddDate(1, 0, 0).Add(-time.Hour),
		sundayAt(3, 14).AddDate(0, 0, 7),
		sundayAt(3, 30).AddDate(0, 0, 7),
	}
	for _, now := range jumps {
		if err := runAt(t, &deps, now); err != nil {
			t.Fatal(err)
		}
	}
	if etcd.defragments() != 1 {
		t.Fatalf("a clock jump gave a run: %s", etcd.seen())
	}
}

func TestRunEtcdDefragIgnoresAHostWithoutTheControlPlaneRole(t *testing.T) {
	createControlPlaneHost(t, "defrag-b", v1alpha1.RoleWorkload)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000"}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(3, 0))
	if err := runAt(t, &deps, sundayAt(3, 16)); err != nil {
		t.Fatal(err)
	}
	if etcd.seen() != "" {
		t.Fatalf("requests %s", etcd.seen())
	}
}

func hostWithRoles(name string, roles ...string) v1alpha1.Host {
	return v1alpha1.Host{Spec: v1alpha1.HostSpec{Roles: roles}, ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestRunEtcdDefragSkipsTheWeekWhenAnotherMemberIsNotHealthy(t *testing.T) {
	controllers(t)
	down := &fakeEtcd{failHeal: true}
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000", extra: []string{startEtcd(t, down).URL}}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(3, 0))
	err := runAt(t, &deps, sundayAt(3, 16))
	if err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("the skip must say why: %v", err)
	}
	if etcd.defragments() != 0 {
		t.Fatalf("defragmented with a sick member: %s", etcd.seen())
	}
	down.mu.Lock()
	down.failHeal = false
	down.mu.Unlock()
	if err := runAt(t, &deps, sundayAt(3, 20)); err != nil || etcd.defragments() != 0 {
		t.Fatalf("the slot of the week is spent: %v %s", err, etcd.seen())
	}
	if err := runAt(t, &deps, sundayAt(3, 16).AddDate(0, 0, 7)); err != nil || etcd.defragments() != 1 {
		t.Fatalf("next week must run: %v %s", err, etcd.seen())
	}
}

func TestRunEtcdDefragSkipsTheWeekWhenAMemberHasNoClientURL(t *testing.T) {
	controllers(t)
	etcd := &fakeEtcd{size: "1900000000", inUse: "77000000", extra: []string{""}}
	deps := defragDeps(t, startEtcd(t, etcd), "defrag-b", sundayAt(3, 0))
	if err := runAt(t, &deps, sundayAt(3, 16)); err == nil || etcd.defragments() != 0 {
		t.Fatalf("a member that did not start is not healthy: %v %s", err, etcd.seen())
	}
}
