package settings

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
)

func egressDefinitions() []Definition {
	return []Definition{
		{"dns.upstreams", "", "IP addresses, separated by spaces, that the cluster CoreDNS forwards every name to. Empty makes CoreDNS use the resolv.conf of the node.", ipAddressList},
		{"egress.http-proxy", "", "HTTP proxy that cert-manager and CDI use to reach external services. Empty means no proxy.", httpURL},
		{"egress.no-proxy", "", "Comma list of hosts, domains and networks that bypass egress.http-proxy.", noProxyList},
		{"letsencrypt.dns01-nameservers", "", "Comma list of host:port servers or https DNS-over-HTTPS URLs that cert-manager asks for the DNS-01 self check. cert-manager asks only these servers. Empty keeps the resolver of the node.", dns01Nameservers},
	}
}

func ipAddressList(v string) error {
	if v == "" {
		return nil
	}
	fields := strings.Fields(v)
	if strings.Join(fields, " ") != v {
		return fmt.Errorf("value %q must separate the addresses with single spaces", v)
	}
	for _, field := range fields {
		if _, err := netip.ParseAddr(field); err != nil {
			return fmt.Errorf("value %q is not a space list of IP addresses", v)
		}
	}
	return nil
}

func noProxyList(v string) error {
	return entryList("no-proxy", func(entry string) bool {
		return entry != "" && !strings.ContainsFunc(entry, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	})(v)
}

func dns01Nameservers(v string) error {
	return entryList("DNS-01 nameserver", func(entry string) bool {
		if _, err := netip.ParseAddrPort(entry); err == nil {
			return true
		}
		parsed, err := url.Parse(entry)
		return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil
	})(v)
}
