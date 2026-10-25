package settings

import "testing"

func TestEgressCatalogKeysAndDefaults(t *testing.T) {
	for _, key := range []string{"dns.upstreams", "egress.http-proxy", "egress.no-proxy", "letsencrypt.dns01-nameservers"} {
		got, ok := Lookup(key)
		if !ok || got.Default != "" || got.Description == "" {
			t.Fatalf("setting %q: %+v", key, got)
		}
	}
}

func TestEgressSettingsValidate(t *testing.T) {
	cases := []struct {
		key   string
		value string
		valid bool
	}{
		{"dns.upstreams", "", true},
		{"dns.upstreams", "217.218.127.127", true},
		{"dns.upstreams", "217.218.127.127 217.218.155.155", true},
		{"dns.upstreams", "2001:4860:4860::8888 8.8.8.8", true},
		{"dns.upstreams", "217.218.127.127,217.218.155.155", false},
		{"dns.upstreams", "217.218.127.127  217.218.155.155", false},
		{"dns.upstreams", " 217.218.127.127", false},
		{"dns.upstreams", "dns.example.com", false},
		{"dns.upstreams", "/etc/resolv.conf", false},
		{"egress.http-proxy", "", true},
		{"egress.http-proxy", "http://proxy.kube-system:10808", true},
		{"egress.http-proxy", "https://proxy.example.com:3128", true},
		{"egress.http-proxy", "socks5://proxy:1080", false},
		{"egress.http-proxy", "proxy.kube-system:10808", false},
		{"egress.no-proxy", "", true},
		{"egress.no-proxy", "10.0.0.0/8,172.16.0.0/12,.cloudyfolks.com,cluster.local,.svc", true},
		{"egress.no-proxy", "10.0.0.0/8, 172.16.0.0/12", false},
		{"egress.no-proxy", "10.0.0.0/8,", false},
		{"letsencrypt.dns01-nameservers", "", true},
		{"letsencrypt.dns01-nameservers", "https://one.one.one.one/dns-query", true},
		{"letsencrypt.dns01-nameservers", "1.1.1.1:53,8.8.8.8:53", true},
		{"letsencrypt.dns01-nameservers", "[2606:4700:4700::1111]:53", true},
		{"letsencrypt.dns01-nameservers", "1.1.1.1", false},
		{"letsencrypt.dns01-nameservers", "http://one.one.one.one/dns-query", false},
		{"letsencrypt.dns01-nameservers", "https://user@one.one.one.one/dns-query", false},
		{"letsencrypt.dns01-nameservers", "1.1.1.1:53,", false},
	}
	for _, tc := range cases {
		err := validates(t, tc.key, tc.value)
		if (err == nil) != tc.valid {
			t.Errorf("%s %q: valid=%v error=%v", tc.key, tc.value, tc.valid, err)
		}
	}
}
