package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
)

type fakePrometheus struct {
	server  *httptest.Server
	mu      sync.Mutex
	queries []url.Values
	status  int
	body    string
	delay   time.Duration
}

func newFakePrometheus(t *testing.T) *fakePrometheus {
	t.Helper()
	prom := &fakePrometheus{status: http.StatusOK, body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`}
	prom.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prom.mu.Lock()
		prom.queries = append(prom.queries, r.URL.Query())
		status, body, delay := prom.status, prom.body, prom.delay
		prom.mu.Unlock()
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(delay)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(prom.server.Close)
	return prom
}

func (p *fakePrometheus) seen() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values{}, p.queries...)
}

func (p *fakePrometheus) url(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse(p.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

type metricsFixture struct {
	prom    *fakePrometheus
	api     *reviewAPI
	handler http.Handler
}

func newMetricsFixture(t *testing.T, allow func(authorizationv1.ResourceAttributes) bool) *metricsFixture {
	t.Helper()
	prom := newFakePrometheus(t)
	api := newReviewAPI(t, allow)
	handler := asCaller(MetricsHandler(api.upstream(t), prom.url(t), time.Now), "caller-token")
	return &metricsFixture{prom: prom, api: api, handler: handler}
}

func metricsRequest(handler http.Handler, query, namespace, name, start, end, step string) *httptest.ResponseRecorder {
	values := url.Values{"query": {query}, "namespace": {namespace}, "name": {name}, "start": {start}, "end": {end}, "step": {step}}
	return do(handler, httptest.NewRequest(http.MethodGet, PathMetrics+"?"+values.Encode(), nil))
}

const (
	rangeStart = "2026-10-08T00:00:00Z"
	rangeEnd   = "2026-10-08T01:00:00Z"
)

func TestMetricsBuildsTheNamedQuery(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	rate := func(metric string) string {
		return `sum(rate(` + metric + `{namespace="acme-web",name="vm1"}[2m]))`
	}
	devices := `device!~"lo|veth.*|genev_sys.*|ovn0|br-int"`
	want := map[string]string{
		"vm.cpu":          rate("kubevirt_vmi_cpu_usage_seconds_total"),
		"vm.memory":       `kubevirt_vmi_memory_resident_bytes{namespace="acme-web",name="vm1"}`,
		"vm.network.rx":   rate("kubevirt_vmi_network_receive_bytes_total"),
		"vm.network.tx":   rate("kubevirt_vmi_network_transmit_bytes_total"),
		"vm.disk.read":    rate("kubevirt_vmi_storage_read_traffic_bytes_total"),
		"vm.disk.write":   rate("kubevirt_vmi_storage_write_traffic_bytes_total"),
		"volume.used":     `kubelet_volume_stats_used_bytes{namespace="acme-web",persistentvolumeclaim="vm1"}`,
		"volume.capacity": `kubelet_volume_stats_capacity_bytes{namespace="acme-web",persistentvolumeclaim="vm1"}`,
		"host.cpu":        `1 - avg(rate(node_cpu_seconds_total{mode="idle",nodename="vm1"}[2m]))`,
		"host.memory":     `1 - node_memory_MemAvailable_bytes{nodename="vm1"} / node_memory_MemTotal_bytes{nodename="vm1"}`,
		"host.network.rx": `sum(rate(node_network_receive_bytes_total{nodename="vm1",` + devices + `}[2m]))`,
		"host.network.tx": `sum(rate(node_network_transmit_bytes_total{nodename="vm1",` + devices + `}[2m]))`,
		"ceph.used":       `ceph_cluster_total_used_bytes`,
		"ceph.capacity":   `ceph_cluster_total_bytes`,
	}
	if len(want) != len(queryTable()) {
		t.Fatalf("table holds %d rows, test holds %d", len(queryTable()), len(want))
	}
	for query, promQL := range want {
		before := len(fx.prom.seen())
		if rec := metricsRequest(fx.handler, query, "acme-web", "vm1", rangeStart, rangeEnd, "60"); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		seen := fx.prom.seen()
		if len(seen) != before+1 {
			t.Fatalf("%s: Prometheus saw %d calls", query, len(seen)-before)
		}
		got := seen[before]
		if got.Get("query") != promQL || got.Get("start") != rangeStart || got.Get("end") != rangeEnd || got.Get("step") != "60" {
			t.Errorf("%s: got %v, want query %s", query, got, promQL)
		}
	}
}

func TestMetricsRefuseUnvalidatedLabels(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	badNames := []string{`vm"}) or vector(1) #`, "a b", "A", "-x", "", strings.Repeat("a", 254), "x\n", "a_b", "x}"}
	for _, name := range badNames {
		if rec := metricsRequest(fx.handler, "vm.cpu", "acme-web", name, rangeStart, rangeEnd, "60"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_query"`) {
			t.Errorf("name %q: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	badNamespaces := []string{"a.b", `a"b`, "a{b", "a\nb", strings.Repeat("a", 64), "", "A"}
	for _, namespace := range badNamespaces {
		if rec := metricsRequest(fx.handler, "vm.cpu", namespace, "vm1", rangeStart, rangeEnd, "60"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_query"`) {
			t.Errorf("namespace %q: %d %s", namespace, rec.Code, rec.Body.String())
		}
	}
	if rec := metricsRequest(fx.handler, "host.cpu", "", `n"}`, rangeStart, rangeEnd, "60"); rec.Code != http.StatusBadRequest {
		t.Errorf("host name: %d", rec.Code)
	}
	if len(fx.prom.seen()) != 0 || len(fx.api.reviews()) != 0 {
		t.Fatalf("Prometheus saw %d calls, API saw %d reviews", len(fx.prom.seen()), len(fx.api.reviews()))
	}
}

func TestMetricsUnknownQuery(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	for _, query := range []string{"up", "", `sum(rate(up[1m]))`} {
		if rec := metricsRequest(fx.handler, query, "acme-web", "vm1", rangeStart, rangeEnd, "60"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"unknown_query"`) {
			t.Errorf("query %q: %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if len(fx.prom.seen()) != 0 {
		t.Fatal("a raw query reached Prometheus")
	}
}

func TestMetricsScopeFollowsRBAC(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	metricsRequest(fx.handler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60")
	metricsRequest(fx.handler, "volume.used", "acme-web", "data", rangeStart, rangeEnd, "60")
	metricsRequest(fx.handler, "host.cpu", "", "node1", rangeStart, rangeEnd, "60")
	metricsRequest(fx.handler, "ceph.used", "", "", rangeStart, rangeEnd, "60")
	group := "bedrock.cloudyfolks.io"
	want := []authorizationv1.ResourceAttributes{
		{Verb: "get", Group: group, Resource: "virtualmachines", Namespace: "acme-web", Name: "vm1"},
		{Verb: "get", Group: group, Resource: "volumes", Namespace: "acme-web", Name: "data"},
		{Verb: "get", Group: group, Resource: "hosts", Name: "node1"},
		{Verb: "get", Group: group, Resource: "clusters", Name: "cluster"},
	}
	got := fx.api.reviews()
	if len(got) != len(want) {
		t.Fatalf("reviews %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("review %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	denied := newMetricsFixture(t, func(authorizationv1.ResourceAttributes) bool { return false })
	if rec := metricsRequest(denied.handler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60"); rec.Code != http.StatusForbidden || len(denied.prom.seen()) != 0 {
		t.Fatalf("denied: %d, Prometheus calls %d", rec.Code, len(denied.prom.seen()))
	}
}

func TestMetricsRangeLimits(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	cases := []struct {
		name, start, end, step, code string
	}{
		{"over 31 days", "2026-08-01T00:00:00Z", "2026-09-05T00:00:00Z", "3600", "range_too_large"},
		{"step 0", rangeStart, rangeEnd, "0", "invalid_query"},
		{"step 3601", rangeStart, rangeEnd, "3601", "invalid_query"},
		{"too many points", "2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z", "60", "range_too_large"},
		{"end before start", rangeEnd, rangeStart, "60", "invalid_query"},
		{"bad start", "yesterday", rangeEnd, "60", "invalid_query"},
		{"bad end", rangeStart, "", "60", "invalid_query"},
		{"bad step", rangeStart, rangeEnd, "x", "invalid_query"},
	}
	for _, tc := range cases {
		rec := metricsRequest(fx.handler, "vm.cpu", "acme-web", "vm1", tc.start, tc.end, tc.step)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	unix := strconv.FormatInt(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).Unix(), 10)
	later := strconv.FormatInt(time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC).Unix(), 10)
	if rec := metricsRequest(fx.handler, "vm.cpu", "acme-web", "vm1", unix, later, "60"); rec.Code != http.StatusOK {
		t.Errorf("unix seconds: %d %s", rec.Code, rec.Body.String())
	}
	if len(fx.prom.seen()) != 1 {
		t.Fatalf("Prometheus saw %d calls", len(fx.prom.seen()))
	}
}

func TestMetricsPassesThePrometheusAnswer(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	fx.prom.body = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1,"2"]]}]}}`
	rec := metricsRequest(fx.handler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60")
	if rec.Code != http.StatusOK || rec.Body.String() != fx.prom.body || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

func TestMetricsPrometheusDown(t *testing.T) {
	api := newReviewAPI(t, allowAll)
	prom := newFakePrometheus(t)
	address := prom.url(t)
	prom.server.Close()
	handler := asCaller(MetricsHandler(api.upstream(t), address, time.Now), "caller-token")
	rec := metricsRequest(handler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"metrics_unavailable"`) {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
	}
	slow := newFakePrometheus(t)
	slow.delay = time.Second
	slowHandler := asCaller(metricsHandler(api.upstream(t), slow.url(t), &http.Client{Timeout: 50 * time.Millisecond}), "caller-token")
	rec = metricsRequest(slowHandler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"metrics_unavailable"`) {
		t.Fatalf("timeout: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMetricsPrometheusErrorStatus(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	fx.prom.status = http.StatusBadRequest
	fx.prom.body = `{"status":"error","errorType":"bad_data","error":"parse error at secret"}`
	rec := metricsRequest(fx.handler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"upstream_unavailable"`) || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMetricsOnlyGet(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	req := httptest.NewRequest(http.MethodPost, PathMetrics, nil)
	if rec := do(fx.handler, req); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("%d", rec.Code)
	}
}

func TestMetricsBodyIsLimited(t *testing.T) {
	fx := newMetricsFixture(t, allowAll)
	fx.prom.body = `{"status":"success","pad":"` + strings.Repeat("x", 9<<20) + `"}`
	rec := metricsRequest(fx.handler, "vm.cpu", "acme-web", "vm1", rangeStart, rangeEnd, "60")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("%d", rec.Code)
	}
}
