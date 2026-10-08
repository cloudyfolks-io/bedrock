package proxy

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

const (
	maxRange          = 31 * 24 * time.Hour
	maxStep           = 3600
	maxPoints         = 1500
	maxMetricsBody    = 8 << 20
	prometheusTimeout = 30 * time.Second
	hostDevices       = `device!~"lo|veth.*|genev_sys.*|ovn0|br-int"`
)

var (
	namespaceExpr = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	nameExpr      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

type scopeKind int

const (
	scopeNone scopeKind = iota
	scopeName
	scopeNamespacedName
)

type metricQuery struct {
	template string
	scope    scopeKind
	access   func(namespace, name string) authorizationv1.ResourceAttributes
}

var errBadTime = errors.New("time is not RFC 3339 or unix seconds")

func getOf(resource string, namespaced bool) func(namespace, name string) authorizationv1.ResourceAttributes {
	return func(namespace, name string) authorizationv1.ResourceAttributes {
		attributes := authorizationv1.ResourceAttributes{Verb: "get", Group: v1alpha1.GroupVersion.Group, Resource: resource, Name: name}
		if namespaced {
			attributes.Namespace = namespace
		}
		return attributes
	}
}

func queryTable() map[string]metricQuery {
	vm := getOf("virtualmachines", true)
	volume := getOf("volumes", true)
	host := getOf("hosts", false)
	ceph := func(string, string) authorizationv1.ResourceAttributes {
		return getOf("clusters", false)("", "cluster")
	}
	return map[string]metricQuery{
		"vm.cpu":          {`sum(rate(kubevirt_vmi_cpu_usage_seconds_total{namespace={namespace},name={name}}[2m]))`, scopeNamespacedName, vm},
		"vm.memory":       {`kubevirt_vmi_memory_resident_bytes{namespace={namespace},name={name}}`, scopeNamespacedName, vm},
		"vm.network.rx":   {`sum(rate(kubevirt_vmi_network_receive_bytes_total{namespace={namespace},name={name}}[2m]))`, scopeNamespacedName, vm},
		"vm.network.tx":   {`sum(rate(kubevirt_vmi_network_transmit_bytes_total{namespace={namespace},name={name}}[2m]))`, scopeNamespacedName, vm},
		"vm.disk.read":    {`sum(rate(kubevirt_vmi_storage_read_traffic_bytes_total{namespace={namespace},name={name}}[2m]))`, scopeNamespacedName, vm},
		"vm.disk.write":   {`sum(rate(kubevirt_vmi_storage_write_traffic_bytes_total{namespace={namespace},name={name}}[2m]))`, scopeNamespacedName, vm},
		"volume.used":     {`kubelet_volume_stats_used_bytes{namespace={namespace},persistentvolumeclaim={name}}`, scopeNamespacedName, volume},
		"volume.capacity": {`kubelet_volume_stats_capacity_bytes{namespace={namespace},persistentvolumeclaim={name}}`, scopeNamespacedName, volume},
		"host.cpu":        {`1 - avg(rate(node_cpu_seconds_total{mode="idle",nodename={name}}[2m]))`, scopeName, host},
		"host.memory":     {`1 - node_memory_MemAvailable_bytes{nodename={name}} / node_memory_MemTotal_bytes{nodename={name}}`, scopeName, host},
		"host.network.rx": {`sum(rate(node_network_receive_bytes_total{nodename={name},` + hostDevices + `}[2m]))`, scopeName, host},
		"host.network.tx": {`sum(rate(node_network_transmit_bytes_total{nodename={name},` + hostDevices + `}[2m]))`, scopeName, host},
		"ceph.used":       {`ceph_cluster_total_used_bytes`, scopeNone, ceph},
		"ceph.capacity":   {`ceph_cluster_total_bytes`, scopeNone, ceph},
	}
}

func MetricsHandler(u *Upstream, prometheusURL *url.URL, _ func() time.Time) http.Handler {
	return metricsHandler(u, prometheusURL, &http.Client{Timeout: prometheusTimeout})
}

func metricsHandler(u *Upstream, prometheusURL *url.URL, prometheus *http.Client) http.Handler {
	table := queryTable()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || caller.Token == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			httpjson.WriteError(w, http.StatusMethodNotAllowed, "invalid_request")
			return
		}
		params := r.URL.Query()
		entry, known := table[params.Get("query")]
		if !known {
			httpjson.WriteError(w, http.StatusBadRequest, "unknown_query")
			return
		}
		promQL, attributes, code := render(entry, params.Get("namespace"), params.Get("name"))
		if code != "" {
			httpjson.WriteError(w, http.StatusBadRequest, code)
			return
		}
		if code := validateRange(params.Get("start"), params.Get("end"), params.Get("step")); code != "" {
			httpjson.WriteError(w, http.StatusBadRequest, code)
			return
		}
		reviews, err := kubernetes.NewForConfig(u.RESTConfig(caller.Token))
		if err != nil {
			upstreamFailure(w, r, "build the review client", err)
			return
		}
		allowed, err := reviewAccess(r.Context(), reviews, attributes)
		if err != nil {
			upstreamFailure(w, r, "review the metrics access", err)
			return
		}
		if !allowed {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
		forward(w, r, prometheus, prometheusURL, promQL, params)
	})
}

func render(entry metricQuery, namespace, name string) (string, authorizationv1.ResourceAttributes, string) {
	if entry.scope == scopeNamespacedName && !namespaceExpr.MatchString(namespace) || len(namespace) > 63 {
		return "", authorizationv1.ResourceAttributes{}, "invalid_query"
	}
	if entry.scope != scopeNone && (!nameExpr.MatchString(name) || len(name) > 253) {
		return "", authorizationv1.ResourceAttributes{}, "invalid_query"
	}
	promQL := strings.NewReplacer("{namespace}", strconv.Quote(namespace), "{name}", strconv.Quote(name)).Replace(entry.template)
	return promQL, entry.access(namespace, name), ""
}

func validateRange(rawStart, rawEnd, rawStep string) string {
	start, startErr := parseTime(rawStart)
	end, endErr := parseTime(rawEnd)
	step, stepErr := strconv.Atoi(rawStep)
	if startErr != nil || endErr != nil || stepErr != nil || step < 1 || step > maxStep || end.Before(start) {
		return "invalid_query"
	}
	span := end.Sub(start)
	if span > maxRange || int(span/time.Second)/step+1 > maxPoints {
		return "range_too_large"
	}
	return ""
}

func parseTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, errBadTime
	}
	return time.Unix(seconds, 0), nil
}

func forward(w http.ResponseWriter, r *http.Request, prometheus *http.Client, base *url.URL, promQL string, params url.Values) {
	target := base.JoinPath("/api/v1/query_range")
	target.RawQuery = url.Values{
		"query": {promQL}, "start": {params.Get("start")}, "end": {params.Get("end")}, "step": {params.Get("step")},
	}.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		httpjson.Internal(w, r, "build the prometheus request", err)
		return
	}
	resp, err := prometheus.Do(req)
	if err != nil {
		slog.ErrorContext(r.Context(), "prometheus unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetricsBody+1))
	if err != nil || len(body) > maxMetricsBody || resp.StatusCode != http.StatusOK {
		slog.ErrorContext(r.Context(), "prometheus answer rejected", "status", resp.StatusCode, "error", err)
		httpjson.WriteError(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}
