package release

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSettingVarsDefaultToNodeResolvConfAndNoEgress(t *testing.T) {
	vars := SettingVars(map[string]string{}, "10.0.10.10")
	want := map[string]string{
		VarDNSForward: "/etc/resolv.conf", VarEgressHTTPProxy: "", VarEgressNoProxy: "", VarDNS01NameserversArg: "", VarDNS01OnlyArg: "",
	}
	for name, value := range want {
		if got, ok := vars[name]; !ok || got != value {
			t.Errorf("%s = %q (present %v), want %q", name, got, ok, value)
		}
	}
}

func TestSettingVarsCarryTheSettings(t *testing.T) {
	vars := SettingVars(map[string]string{
		"dns.upstreams":                 "217.218.127.127 217.218.155.155",
		"egress.http-proxy":             "http://proxy.kube-system:10808",
		"egress.no-proxy":               "10.0.0.0/8,.svc",
		"letsencrypt.dns01-nameservers": "https://one.one.one.one/dns-query",
	}, "10.0.10.10")
	want := map[string]string{
		VarDNSForward:          "217.218.127.127 217.218.155.155",
		VarEgressHTTPProxy:     "http://proxy.kube-system:10808",
		VarEgressNoProxy:       "10.16.0.0/16,10.96.0.0/12,cluster.local,.cluster.local,.svc,localhost,127.0.0.1,10.0.10.10,10.0.0.0/8",
		VarDNS01NameserversArg: "--dns01-recursive-nameservers=https://one.one.one.one/dns-query",
		VarDNS01OnlyArg:        "--dns01-recursive-nameservers-only=true",
	}
	for name, value := range want {
		if vars[name] != value {
			t.Errorf("%s = %q, want %q", name, vars[name], value)
		}
	}
}

func TestDNSAddressIsTheTenthAddressOfTheServiceNetwork(t *testing.T) {
	cases := map[string]string{"10.96.0.1": "10.96.0.10", "10.0.0.1": "10.0.0.10", "172.30.0.1": "172.30.0.10"}
	for api, want := range cases {
		got, ok := DNSAddress(api)
		if !ok || got != want {
			t.Errorf("DNSAddress(%q) = %q %v, want %q", api, got, ok, want)
		}
	}
	if _, ok := DNSAddress(""); ok {
		t.Error("an empty address must not give a DNS address")
	}
}

func TestVarsHoldTheDNSAddressAndTheSettings(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "dns.upstreams"}, Spec: v1alpha1.SettingSpec{Value: "9.9.9.9"}}
	if err := c.Create(ctx, setting); err != nil {
		t.Fatal(err)
	}
	vars, err := Vars(ctx, c, "10.0.10.10")
	if err != nil {
		t.Fatal(err)
	}
	if vars[VarDNSIP] != "10.0.0.10" || vars[VarDNSForward] != "9.9.9.9" || vars[VarDNS01OnlyArg] != "" {
		t.Fatalf("vars %v", vars)
	}
}

func TestNoProxyHoldsTheInClusterEntriesFirst(t *testing.T) {
	got := NoProxy("", "10.0.10.10")
	want := "10.16.0.0/16,10.96.0.0/12,cluster.local,.cluster.local,.svc,localhost,127.0.0.1,10.0.10.10"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoProxyAppendsTheSettingAndDropsDuplicates(t *testing.T) {
	got := NoProxy(".svc,.cloudyfolks.com,10.0.10.10,.cloudyfolks.com", "10.0.10.10")
	want := "10.16.0.0/16,10.96.0.0/12,cluster.local,.cluster.local,.svc,localhost,127.0.0.1,10.0.10.10,.cloudyfolks.com"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoProxyWithoutAnAPIAddress(t *testing.T) {
	if got := NoProxy("", ""); strings.Contains(got, ",,") || strings.HasSuffix(got, ",") {
		t.Fatalf("got %q", got)
	}
}

func TestNoProxyIsEmptyWithoutAProxy(t *testing.T) {
	if got := SettingVars(map[string]string{"egress.no-proxy": ".svc"}, "10.0.10.10")[VarEgressNoProxy]; got != "" {
		t.Fatalf("got %q", got)
	}
}
