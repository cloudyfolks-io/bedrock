package release

import (
	"context"
	"net/netip"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/config"
)

const (
	VarDNSIP               = "BEDROCK_DNS_IP"
	VarDNSForward          = "BEDROCK_DNS_FORWARD"
	VarEgressHTTPProxy     = "BEDROCK_EGRESS_HTTP_PROXY"
	VarEgressNoProxy       = "BEDROCK_EGRESS_NO_PROXY"
	VarDNS01NameserversArg = "BEDROCK_DNS01_NAMESERVERS_ARG"
	VarDNS01OnlyArg        = "BEDROCK_DNS01_ONLY_ARG"

	NodeResolvConf = "/etc/resolv.conf"

	kubernetesServiceNamespace = "default"
	kubernetesServiceName      = "kubernetes"
	dnsOffsetFromAPIService    = 9
)

func SettingVars(values map[string]string, apiAddress string) map[string]string {
	forward := strings.TrimSpace(values["dns.upstreams"])
	if forward == "" {
		forward = NodeResolvConf
	}
	vars := map[string]string{
		VarDNSForward:          forward,
		VarEgressHTTPProxy:     values["egress.http-proxy"],
		VarEgressNoProxy:       egressNoProxy(values, apiAddress),
		VarDNS01NameserversArg: "",
		VarDNS01OnlyArg:        "",
	}
	if nameservers := values["letsencrypt.dns01-nameservers"]; nameservers != "" {
		vars[VarDNS01NameserversArg] = "--dns01-recursive-nameservers=" + nameservers
		vars[VarDNS01OnlyArg] = "--dns01-recursive-nameservers-only=true"
	}
	return vars
}

func egressNoProxy(values map[string]string, apiAddress string) string {
	if values["egress.http-proxy"] == "" {
		return ""
	}
	return NoProxy(values["egress.no-proxy"], apiAddress)
}

func NoProxy(setting, apiAddress string) string {
	entries := []string{config.DefaultPodCIDR, config.DefaultServiceCIDR, "cluster.local", ".cluster.local", ".svc", "localhost", "127.0.0.1", apiAddress}
	if setting != "" {
		entries = append(entries, strings.Split(setting, ",")...)
	}
	kept := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry != "" && !slices.Contains(kept, entry) {
			kept = append(kept, entry)
		}
	}
	return strings.Join(kept, ",")
}

func DNSAddress(apiServiceIP string) (string, bool) {
	addr, err := netip.ParseAddr(apiServiceIP)
	if err != nil {
		return "", false
	}
	for range dnsOffsetFromAPIService {
		addr = addr.Next()
	}
	return addr.String(), true
}

func settingValues(ctx context.Context, c client.Client) (map[string]string, error) {
	var list v1alpha1.SettingList
	err := c.List(ctx, &list)
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(list.Items))
	for _, setting := range list.Items {
		values[setting.Name] = setting.Spec.Value
	}
	return values, nil
}

func dnsVars(ctx context.Context, c client.Client) (map[string]string, error) {
	var service corev1.Service
	err := c.Get(ctx, client.ObjectKey{Namespace: kubernetesServiceNamespace, Name: kubernetesServiceName}, &service)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if address, ok := DNSAddress(service.Spec.ClusterIP); ok {
		return map[string]string{VarDNSIP: address}, nil
	}
	return nil, nil
}
